package downloader

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/mpd"
)

type Stream interface {
	saveTo(ctx context.Context, logger zerolog.Logger, accessToken string, fileName string) error
}

func (d *Downloader) getStream(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) (s Stream, err error) {
	trackURL := fmt.Sprintf(trackStreamAPIFormat, id)
	reqURL, err := url.Parse(trackURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse track URL to build track stream URLs")
		return nil, fmt.Errorf("failed to parse track URL to build track stream URLs: %v", err)
	}

	params := make(url.Values, 6)
	params.Add("countryCode", "US")
	params.Add("audioquality", "HI_RES_LOSSLESS")
	params.Add("playbackmode", "STREAM")
	params.Add("assetpresentation", "FULL")
	params.Add("immersiveaudio", "false")
	params.Add("locale", "en")

	reqURL.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track stream URLs request")
		return nil, fmt.Errorf("failed to create get track stream URLs request: %v", err)
	}

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetStreamURLs) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track stream URLs request")
		return nil, fmt.Errorf("failed to send get stream URLs request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track stream URLs response body")
			err = errors.Join(err, fmt.Errorf("failed to close get track stream URLs response body: %v", closeErr))
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
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return nil, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return nil, fmt.Errorf("failed to read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return nil, fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, fmt.Errorf("failed to read 200 response body: %w", err)
	}

	var respBody struct {
		ManifestMimeType string `json:"manifestMimeType"`
		Manifest         string `json:"manifest"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
		return nil, fmt.Errorf("failed to decode 200 response body: %w", err)
	}

	switch mimeType := respBody.ManifestMimeType; mimeType {
	case "application/dash+xml", "dash+xml":
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Manifest))
		info, err := mpd.ParseStreamInfo(dec)
		if nil != err {
			logger.Error().Err(err).Str("manifest", respBody.Manifest).Msg("Failed to parse stream info")
			return nil, fmt.Errorf("failed to parse stream info: %v", err)
		}

		return &DashTrackStream{
			Info:            *info,
			DownloadTimeout: time.Duration(d.conf.Timeouts.DownloadDashSegment) * time.Second,
		}, nil
	case "application/vnd.tidal.bts", "vnd.tidal.bt":
		var manifest VNDManifest
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Manifest))
		if err := json.NewDecoder(dec).Decode(&manifest); nil != err {
			logger.Error().Err(err).Str("manifest", respBody.Manifest).Msg("Failed to decode vnd.tidal.bt manifest")
			return nil, fmt.Errorf("failed to decode vnd.tidal.bt manifest: %v", err)
		}

		switch manifest.EncryptionType {
		case "NONE":
		default:
			return nil, fmt.Errorf(
				"encrypted vnd.tidal.bt manifest is not yet implemented: %s",
				manifest.EncryptionType,
			)
		}

		if len(manifest.URLs) == 0 {
			return nil, errors.New("empty vnd.tidal.bt manifest URLs")
		}

		return &VndTrackStream{
			URL:                     manifest.URLs[0],
			DownloadTimeout:         time.Duration(d.conf.Timeouts.DownloadVNDSegment) * time.Second,
			GetTrackFileSizeTimeout: time.Duration(d.conf.Timeouts.GetVNDTrackFileSize) * time.Second,
		}, nil
	default:
		return nil, fmt.Errorf("unexpected manifest mime type: %s", mimeType)
	}
}
