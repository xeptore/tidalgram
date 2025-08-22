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

	"github.com/xeptore/tidalgram/must"
	"github.com/xeptore/tidalgram/tidal/fs"
)

type LoginLink struct {
	URL       string
	ExpiresIn time.Duration
}

func (a *Auth) InitiateLoginFlow(ctx context.Context) (*LoginLink, <-chan error, error) {
	res, err := issueAuthorizationRequest(ctx)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return nil, nil, context.Canceled
		}

		return nil, nil, fmt.Errorf("failed to issue authorization request: %v", err)
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
				creds, err := res.poll(ctx)
				if nil != err {
					if errors.Is(err, ErrUnauthorized) {
						continue waitloop
					}

					if errors.Is(err, context.DeadlineExceeded) {
						// TODO: log the error with a hint to increase the poll request timeout
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

func issueAuthorizationRequest(ctx context.Context) (out *authorizationResponse, err error) {
	reqURL, err := url.JoinPath(baseURL, "/device_authorization")
	must.Be(nil == err, "device authorization URL must be a valid URL")

	reqParams := make(url.Values, 2)
	reqParams.Add("client_id", clientID)
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(reqParamsStr))
	if nil != err {
		return nil, fmt.Errorf("failed to create device authorization request %s: %v", reqURL, err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}

		return nil, fmt.Errorf("failed to issue device authorization request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read 200 response body: %v", err)
	}

	var respBody struct {
		DeviceCode      string `json:"deviceCode"`
		UserCode        string `json:"userCode"`
		VerificationURI string `json:"verificationUri"`
		ExpiresIn       int    `json:"expiresIn"`
		Interval        int    `json:"interval"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode 200 response body: %v", err)
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

func (r *authorizationResponse) poll(ctx context.Context) (*Credentials, error) {
	reqURL, err := url.JoinPath(baseURL, "/token")
	must.Be(nil == err, "token URL must be a valid URL")

	reqParams := make(url.Values, 4)
	reqParams.Add("client_id", clientID)
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParams.Add("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	reqParams.Add("device_code", r.DeviceCode)
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(reqParamsStr))
	if nil != err {
		return nil, fmt.Errorf("failed to create token request %s: %v", reqURL, err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add(
		"Authorization",
		"Basic "+base64.StdEncoding.Strict().EncodeToString([]byte(clientID+":"+clientSecret)),
	)

	client := http.Client{Timeout: 10 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}

		return nil, fmt.Errorf("failed to issue token request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusBadRequest:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 400 response body: %v", err)
		}

		var respBody struct {
			Status           int    `json:"status"`
			Error            string `json:"error"`
			SubStatus        int    `json:"sub_status"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(respBytes, &respBody); nil != err {
			return nil, fmt.Errorf("failed to decode 400 response body: %v", err)
		}

		if respBody.Status == 400 &&
			respBody.Error == "authorization_pending" &&
			respBody.SubStatus == 1002 &&
			respBody.ErrorDescription == "Device Authorization code is not authorized yet" {
			return nil, ErrUnauthorized
		}

		return nil, fmt.Errorf("unexpected 400 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read response body: %v", err)
		}

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read 200 response body: %v", err)
	}

	var respBody struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode 200 response body: %v", err)
	}

	expiresAt, err := extractExpiresAt(respBody.AccessToken)
	if nil != err {
		return nil, fmt.Errorf("failed to decode 200 response body: %v", err)
	}

	return &Credentials{
		Token:        respBody.AccessToken,
		RefreshToken: respBody.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}
