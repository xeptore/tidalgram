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

	"github.com/xeptore/tidalgram/httputil"
)

func (a *Auth) RefreshToken(ctx context.Context, logger zerolog.Logger) error {
	newCreds, err := a.refreshToken(ctx, logger)
	if nil != err {
		return fmt.Errorf("failed to refresh token: %w", err)
	}
	a.credentials.Store(&Credentials{
		Token:        newCreds.Token,
		RefreshToken: newCreds.RefreshToken,
		ExpiresAt:    newCreds.ExpiresAt,
	})

	return nil
}

func (a *Auth) refreshToken(ctx context.Context, logger zerolog.Logger) (creds *Credentials, err error) {
	reqURL, err := url.JoinPath(baseURL, "/token")
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join base URL and token path")
		return nil, fmt.Errorf("failed to join base URL and token path: %v", err)
	}

	reqParams := make(url.Values, 4)
	reqParams.Add("client_id", clientID)
	refreshToken := a.credentials.Load().RefreshToken
	reqParams.Add("refresh_token", refreshToken)
	reqParams.Add("grant_type", "refresh_token")
	reqParams.Add("scope", "r_usr+w_usr+w_sub")
	reqParamsStr := reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBufferString(reqParamsStr))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create refresh token request")
		return nil, fmt.Errorf("failed to create refresh token request: %w", err)
	}

	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Accept", "application/json")
	req.Header.Add(
		"Authorization",
		"Basic "+base64.StdEncoding.Strict().EncodeToString([]byte(clientID+":"+clientSecret)),
	)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to issue refresh token request")
		return nil, fmt.Errorf("failed to issue refresh token request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close response body")
			err = errors.Join(err, fmt.Errorf("failed to close response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return nil, fmt.Errorf("failed to read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return nil, fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %w", err)
		} else if ok {
			return nil, ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return nil, fmt.Errorf("received unknown 401 response with body: %s", string(respBytes))
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

		if respBody.Status == 400 && respBody.SubStatus == 11101 &&
			respBody.Error == "invalid_grant" &&
			respBody.ErrorDescription == "Token could not be verified" {
			return nil, ErrUnauthorized
		}

		logger.Error().
			Int("status", respBody.Status).
			Str("error", respBody.Error).
			Int("sub_status", respBody.SubStatus).
			Str("error_description", respBody.ErrorDescription).
			Bytes("response_body", respBytes).
			Msg("Unexpected 400 response")

		return nil, fmt.Errorf("received unknown 400 response with body: %s", string(respBytes))
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
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, fmt.Errorf("failed to read 200 response body: %w", err)
	}

	var respBody struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
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
