package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/xeptore/tidalgram/httputil"
)

func (a *Auth) TryRefreshToken(ctx context.Context) error {
	select {
	case a.refreshSem <- struct{}{}:
		defer func() { <-a.refreshSem }()
		newCreds, err := a.refreshToken(ctx)
		if nil != err {
			if errors.Is(err, ErrUnauthorized) {
				return ErrUnauthorized
			}

			return fmt.Errorf("failed to initiate login flow: %v", err)
		}
		a.credentials.Store(&Credentials{
			Token:        newCreds.Token,
			RefreshToken: newCreds.RefreshToken,
			ExpiresAt:    newCreds.ExpiresAt,
		})

		return nil
	default:
		return ErrTokenRefreshInProgress
	}
}

func (a *Auth) refreshToken(ctx context.Context) (*Credentials, error) {
	reqURL, err := url.JoinPath(baseURL, "/token")
	if nil != err {
		return nil, fmt.Errorf("failed to create token verification URL: %v", err)
	}

	reqParams := make(url.Values, 4)
	reqParams.Add("client_id", clientID)
	refreshToken := a.credentials.Load().RefreshToken
	reqParams.Add("refresh_token", refreshToken)
	reqParams.Add("grant_type", "refresh_token")
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		reqURL,
		bytes.NewBufferString(reqParamsStr),
	)
	if nil != err {
		return nil, fmt.Errorf("failed to create refresh token request: %v", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add(
		"Authorization",
		"Basic "+base64.StdEncoding.Strict().EncodeToString([]byte(clientID+":"+clientSecret)),
	)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		return nil, fmt.Errorf("failed to issue refresh token request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := httputil.ReadResponseBody(resp)
		if nil != err {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if token is expired: %w", err)
		} else if ok {
			return nil, ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if token is invalid: %w", err)
		} else if ok {
			return nil, ErrUnauthorized
		}

		return nil, errors.New("received 401 response")
	case http.StatusBadRequest:
		respBytes, err := httputil.ReadResponseBody(resp)
		if nil != err {
			return nil, fmt.Errorf("failed to read response body: %w", err)
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
		if respBody.Status == 400 && respBody.SubStatus == 11101 &&
			respBody.Error == "invalid_grant" &&
			respBody.ErrorDescription == "Token could not be verified" {
			return nil, ErrUnauthorized
		}

		return nil, errors.New("unexpected 400 response")
	default:
		return nil, fmt.Errorf("unexpected status code: %d", code)
	}

	respBytes, err := httputil.ReadResponseBody(resp)
	if nil != err {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	var respBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode 200 status code response body: %w", err)
	}

	expiresAt, err := extractExpiresAt(respBody.AccessToken)
	if nil != err {
		return nil, fmt.Errorf("failed to decode 200 status code response body: %w", err)
	}

	return &Credentials{
		Token:        respBody.AccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}
