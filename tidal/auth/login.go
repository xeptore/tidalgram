package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/must"
	"github.com/xeptore/tidalgram/tidal/fs"
)

type LoginLink struct {
	URL       string
	ExpiresIn time.Duration
}

func (a *Auth) InitiateLoginFlow(ctx context.Context, logger zerolog.Logger) (*LoginLink, <-chan error, error) {
	res, err := issueAuthorizationRequest(ctx, logger)
	if nil != err {
		return nil, nil, fmt.Errorf("failed to issue authorization request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(res.ExpiresIn)*time.Second)
	ticker := time.NewTicker(time.Duration(res.Interval) * time.Second * 5)
	done := make(chan error)

	go func() {
		defer close(done)
		defer ticker.Stop()
		defer cancel()

	waitloop:
		for {
			select {
			case <-ctx.Done():
				err := ctx.Err()
				// ctx.Err() can never be nil in a ctx.Done block
				if errors.Is(err, context.DeadlineExceeded) {
					done <- ErrLoginLinkExpired
					return
				}

				if errors.Is(err, context.Canceled) {
					done <- context.Canceled
					return
				}

				panic("unexpected context error in initiate login flow")
			case <-ticker.C:
				creds, err := res.poll(ctx, logger)
				if nil != err {
					if errors.Is(err, ErrUnauthorized) {
						continue waitloop
					}

					if errors.Is(err, context.DeadlineExceeded) {
						continue waitloop
					}

					if errors.Is(err, context.Canceled) {
						done <- context.Canceled
						return
					}

					done <- fmt.Errorf("unexpected error from authorization request poll: %v", err)

					return
				}

				a.credentials.Store(&Credentials{
					Token:        creds.Token,
					RefreshToken: creds.RefreshToken,
					ExpiresAt:    creds.ExpiresAt,
				})
				content := fs.AuthFileContent{
					Token:        creds.Token,
					RefreshToken: creds.RefreshToken,
					ExpiresAt:    creds.ExpiresAt.Unix(),
				}
				if err := fs.AuthFileFrom(a.credsDir, tokenFileName).Write(content); nil != err {
					logger.Error().Err(err).Msg("Failed to write credentials to file")
					done <- fmt.Errorf("failed to write credentials to file: %v", err)

					return
				}
				done <- nil

				return
			}
		}
	}()

	return &LoginLink{
		URL:       res.URL,
		ExpiresIn: time.Duration(res.ExpiresIn) * time.Second,
	}, done, nil
}

type authorizationResponse struct {
	URL        string
	DeviceCode string
	ExpiresIn  int
	Interval   int
}

func issueAuthorizationRequest(ctx context.Context, logger zerolog.Logger) (out *authorizationResponse, err error) {
	reqURL, err := url.JoinPath(baseURL, "/device_authorization")
	must.Be(nil == err, "device authorization URL must be a valid URL")

	reqParams := make(url.Values, 2)
	reqParams.Add("client_id", clientID)
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(reqParamsStr))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create device authorization request")
		return nil, fmt.Errorf("failed to create device authorization request %s: %w", reqURL, err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Accept", "application/json")

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to issue device authorization request")
		return nil, fmt.Errorf("failed to issue device authorization request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", resp.StatusCode).Msg("Failed to read response body")
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		logger.
			Error().
			Int("status_code", resp.StatusCode).
			Bytes("response_body", respBytes).
			Msg("Unexpected status code from device authorization request")

		return nil, fmt.Errorf("unexpected status code %d with body: %s", resp.StatusCode, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Int("status_code", resp.StatusCode).Msg("Failed to read response body")
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var respBody struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		ExpiresIn       int    `json:"expiresIn"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
		return nil, fmt.Errorf("failed to decode 200 response body: %w", err)
	}

	//nolint:exhaustruct
	authorizationURL := url.URL{
		Scheme: "https",
		Host:   respBody.VerificationURI,
		Path:   respBody.UserCode,
	}
	authorizationURLStr := authorizationURL.String()

	return &authorizationResponse{
		URL:        authorizationURLStr,
		DeviceCode: respBody.DeviceCode,
		ExpiresIn:  respBody.ExpiresIn,
		Interval:   respBody.Interval,
	}, nil
}

func (r *authorizationResponse) poll(ctx context.Context, logger zerolog.Logger) (*Credentials, error) {
	reqURL, err := url.JoinPath(baseURL, "/token")
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join token URL")
		return nil, fmt.Errorf("failed to join token URL: %v", err)
	}

	reqParams := make(url.Values, 4)
	reqParams.Add("client_id", clientID)
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParams.Add("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	reqParams.Add("device_code", r.DeviceCode)
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(reqParamsStr))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create token request")
		return nil, fmt.Errorf("failed to create token request %s: %w", reqURL, err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Accept", "application/json")
	req.Header.Add(
		"Authorization",
		"Basic "+base64.StdEncoding.Strict().EncodeToString([]byte(clientID+":"+clientSecret)),
	)

	client := http.Client{Timeout: 10 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to issue token request")
		return nil, fmt.Errorf("failed to issue token request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close response body")
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusBadRequest:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 400 response body")
			return nil, fmt.Errorf("failed to read 400 response body: %w", err)
		}

		var respBody struct {
			Status           int    `json:"status"`
			Error            string `json:"error"`
			SubStatus        int    `json:"sub_status"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(respBytes, &respBody); nil != err {
			logger.Error().Err(err).Msg("Failed to decode 400 response body")
			return nil, fmt.Errorf("failed to decode 400 response body: %v", err)
		}

		if respBody.Status == 400 &&
			respBody.Error == "authorization_pending" &&
			respBody.SubStatus == 1002 &&
			respBody.ErrorDescription == "Device Authorization code is not authorized yet" {
			return nil, ErrUnauthorized
		}

		logger.
			Error().
			Int("status", respBody.Status).
			Str("error", respBody.Error).
			Int("sub_status", respBody.SubStatus).
			Str("error_description", respBody.ErrorDescription).
			Bytes("response_body", respBytes).
			Msg("Unexpected 400 response")

		return nil, fmt.Errorf("unexpected 400 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read response body")
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var respBody struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
		return nil, fmt.Errorf("failed to decode 200 response body: %w", err)
	}

	expiresAt, err := extractExpiresAt(respBody.AccessToken)
	if nil != err {
		return nil, fmt.Errorf("failed to extract expires at from access token: %w", err)
	}

	return &Credentials{
		Token:        respBody.AccessToken,
		RefreshToken: respBody.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}
