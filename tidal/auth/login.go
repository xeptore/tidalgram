package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/xeptore/tidalgram/result"
	"github.com/xeptore/tidalgram/tidal/fs"
)

type LoginLink struct {
	URL       string
	ExpiresIn time.Duration
}

func (a *Auth) InitiateLoginFlow(
	ctx context.Context,
) (*LoginLink, <-chan result.Of[Credentials], error) {
	select {
	case a.loginSem <- struct{}{}:
		defer func() { <-a.loginSem }()
		link, wait, err := a.initiateLoginFlow(ctx)
		if nil != err {
			return nil, nil, fmt.Errorf("failed to initiate login flow: %v", err)
		}

		return link, wait, nil
	default:
		return nil, nil, ErrLoginInProgress
	}
}

func (a *Auth) initiateLoginFlow(
	ctx context.Context,
) (*LoginLink, <-chan result.Of[Credentials], error) {
	res, err := issueAuthorizationRequest(ctx)
	if nil != err {
		return nil, nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, (time.Duration(res.ExpiresIn)+1)*time.Second)
	ticker := time.NewTicker(time.Duration(res.Interval) * time.Second * 5)
	done := make(chan result.Of[Credentials])

	go func() {
		defer close(done)
		defer ticker.Stop()
		defer cancel()
	waitloop:
		for {
			select {
			case <-ctx.Done():
				err := ctx.Err()
				if errors.Is(err, context.DeadlineExceeded) {
					done <- result.Err[Credentials](ErrLoginLinkExpired)

					return
				}
				done <- result.Err[Credentials](fmt.Errorf("authorization wait context errored with unknown error: %v", err))

				return
			case <-ticker.C:
				creds, err := res.poll(ctx)
				if nil != err {
					switch {
					case errors.Is(ctx.Err(), context.Canceled):
						done <- result.Err[Credentials](context.Canceled)

						return
					case errors.Is(err, context.Canceled):
						panic("Unexpected poller context cancellation when an error is already returned from it")
					case errors.Is(err, context.DeadlineExceeded):
						// The poller has timed out, not the auth-wait context
						done <- result.Err[Credentials](errors.New("failed to poll authorization status due to timeout"))

						return
					case errors.Is(err, ErrUnauthorized):
						continue waitloop
					default:
						panic(err)
					}
				}
				content := fs.AuthFileContent{
					Token:        creds.Token,
					RefreshToken: creds.RefreshToken,
					ExpiresAt:    creds.ExpiresAt.Unix(),
				}
				if err := fs.AuthFileFrom(a.credsDir, tokenFileName).Write(content); nil != err {
					done <- result.Err[Credentials](err)

					return
				}
				done <- result.Ok(&Credentials{
					Token:        creds.Token,
					RefreshToken: creds.RefreshToken,
					ExpiresAt:    creds.ExpiresAt,
				})

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
	if nil != err {
		return nil, fmt.Errorf("failed to create device authorization URL: %v", err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("client_id", clientID)
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		reqURL,
		bytes.NewBufferString(reqParamsStr),
	)
	if nil != err {
		return nil, fmt.Errorf("failed to create device authorization request: %v", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		return nil, fmt.Errorf("failed to issue device authorization request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
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
		return nil, fmt.Errorf("failed to decode response body: %w", err)
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
	// Create a detached context which is only canceled when parent is canceled, not when parent's deadline exceeded.
	pollCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				// Ignore
			case errors.Is(err, context.Canceled):
				cancel()

				return
			default:
				panic("unexpected error value for ended parent context:" + err.Error())
			}
		case <-pollCtx.Done():
			// When outer function returns
			return
		}
	}()

	reqURL, err := url.JoinPath(baseURL, "/token")
	if nil != err {
		return nil, fmt.Errorf("failed to create token URL: %v", err)
	}

	reqParams := make(url.Values, 4)
	reqParams.Add("client_id", clientID)
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParams.Add("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	reqParams.Add("device_code", r.DeviceCode)
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(
		pollCtx,
		http.MethodPost,
		reqURL,
		bytes.NewBufferString(reqParamsStr),
	)
	if nil != err {
		return nil, fmt.Errorf("failed to create token request: %v", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add(
		"Authorization",
		"Basic "+base64.StdEncoding.Strict().EncodeToString([]byte(clientID+":"+clientSecret)),
	)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
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
			return nil, fmt.Errorf("failed to read response body: %v", err)
		}
		var respBody struct {
			Status           int    `json:"status"`
			Error            string `json:"error"`
			SubStatus        int    `json:"sub_status"`
			ErrorDescription string `json:"error_description"`
		}
		if err := json.Unmarshal(respBytes, &respBody); nil != err {
			return nil, fmt.Errorf("failed to decode 400 status code response body: %v", err)
		}
		if respBody.Status == 400 &&
			respBody.Error == "authorization_pending" &&
			respBody.SubStatus == 1002 &&
			respBody.ErrorDescription == "Device Authorization code is not authorized yet" {
			return nil, ErrUnauthorized
		}

		return nil, errors.New("unexpected 400 response")
	default:
		return nil, fmt.Errorf("unexpected status code: %d", code)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}
	var respBody struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode 200 status code response body: %v", err)
	}

	expiresAt, err := extractExpiresAt(respBody.AccessToken)
	if nil != err {
		return nil, fmt.Errorf("failed to decode 200 status code response body: %w", err)
	}

	return &Credentials{
		Token:        respBody.AccessToken,
		RefreshToken: respBody.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}
