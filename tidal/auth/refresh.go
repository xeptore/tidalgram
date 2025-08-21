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

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/must"
)

func (a *Auth) RefreshToken(ctx context.Context) error {
	newCreds, err := a.refreshToken(ctx)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, ErrUnauthorized) {
			return ErrUnauthorized
		}

		return fmt.Errorf("failed to refresh token: %v", err)
	}
	a.credentials.Store(&Credentials{
		Token:        newCreds.Token,
		RefreshToken: newCreds.RefreshToken,
		ExpiresAt:    newCreds.ExpiresAt,
	})

	return nil
}

func (a *Auth) refreshToken(ctx context.Context) (creds *Credentials, err error) {
	reqURL, err := url.JoinPath(baseURL, "/token")
	must.Be(nil == err, "failed to create token verification URL")

	reqParams := make(url.Values, 4)
	reqParams.Add("client_id", clientID)
	refreshToken := a.credentials.Load().RefreshToken
	reqParams.Add("refresh_token", refreshToken)
	reqParams.Add("grant_type", "refresh_token")
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(reqParamsStr))
	must.Be(nil == err, "failed to create refresh token request")

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add(
		"Authorization",
		"Basic "+base64.StdEncoding.Strict().EncodeToString([]byte(clientID+":"+clientSecret)),
	)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

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
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 401 response body: %v", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, ErrUnauthorized
		}

		return nil, fmt.Errorf("received unknown 401 response with body: %s", string(respBytes))
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
		if respBody.Status == 400 && respBody.SubStatus == 11101 &&
			respBody.Error == "invalid_grant" &&
			respBody.ErrorDescription == "Token could not be verified" {
			return nil, ErrUnauthorized
		}

		return nil, fmt.Errorf("received unknown 400 response with body: %s", string(respBytes))
	default:
		return nil, fmt.Errorf("unexpected status code: %d", code)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read 200 response body: %v", err)
	}
	var respBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode 200 response body: %v", err)
	}

	expiresAt, err := extractExpiresAt(respBody.AccessToken)
	if nil != err {
		return nil, fmt.Errorf("failed to extract expires at from 200 response body access token: %v", err)
	}

	return &Credentials{
		Token:        respBody.AccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}
