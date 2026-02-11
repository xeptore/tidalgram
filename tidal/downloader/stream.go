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
	"github.com/xeptore/tidalgram/tidal/types"
)

type Stream interface {
	saveTo(ctx context.Context, logger zerolog.Logger, accessToken string, fileName string) error
}

func (d *Downloader) getStream(
	ctx context.Context,
	logger zerolog.Logger,
	id string,
) (s Stream, container types.ContainerInfo, err error) {
	reqURL, err := url.Parse(d.conf.HifiAPI)
	if nil != err {
		return nil, types.ContainerInfo{}, fmt.Errorf("parse Hi-Fi API URL: %v", err)
	}
	path, err := url.JoinPath(reqURL.Path, "track")
	if nil != err {
		return nil, types.ContainerInfo{}, fmt.Errorf("join Hi-Fi API URL with track path: %v", err)
	}
	reqURL.Path = path

	reqParams := make(url.Values, 2)
	reqParams.Add("id", id)
	reqParams.Add("quality", "HI_RES_LOSSLESS")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track stream URLs request")
		return nil, types.ContainerInfo{}, fmt.Errorf("create get track stream URLs request: %v", err)
	}

	req.Header.Add("Accept", "application/json")

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetStreamURLs) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track stream URLs request")
		return nil, types.ContainerInfo{}, fmt.Errorf("send get stream URLs request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track stream URLs response body")
			err = errors.Join(err, fmt.Errorf("close get track stream URLs response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return nil, types.ContainerInfo{}, fmt.Errorf("read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return nil, types.ContainerInfo{}, fmt.Errorf("check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, types.ContainerInfo{}, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return nil, types.ContainerInfo{}, fmt.Errorf("check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, types.ContainerInfo{}, auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return nil, types.ContainerInfo{}, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, types.ContainerInfo{}, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return nil, types.ContainerInfo{}, fmt.Errorf("read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return nil, types.ContainerInfo{}, fmt.Errorf("check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, types.ContainerInfo{}, ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return nil, types.ContainerInfo{}, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return nil, types.ContainerInfo{}, fmt.Errorf("read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, types.ContainerInfo{}, fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, types.ContainerInfo{}, fmt.Errorf("read 200 response body: %w", err)
	}

	var respBody struct {
		Data struct {
			ManifestMimeType string `json:"manifestMimeType"`
			Manifest         string `json:"manifest"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
		return nil, types.ContainerInfo{}, fmt.Errorf("decode 200 response body: %w", err)
	}

	switch mimeType := respBody.Data.ManifestMimeType; mimeType {
	case "application/dash+xml", "dash+xml":
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Data.Manifest))
		info, err := mpd.ParseStreamInfo(dec)
		if nil != err {
			logger.Error().Err(err).Str("manifest", respBody.Data.Manifest).Msg("Failed to parse stream info")
			return nil, types.ContainerInfo{}, fmt.Errorf("parse stream info: %v", err)
		}

		container, err := types.ResolveContainer(info.MimeType, info.Codec)
		if nil != err {
			logger.
				Error().
				Err(err).
				Str("mime_type", info.MimeType).
				Str("codec", info.Codec).
				Msg("Failed to infer track extension")

			return nil, types.ContainerInfo{}, fmt.Errorf("infer track extension: %v", err)
		}

		return &DashTrackStream{
			Info:            *info,
			DownloadTimeout: time.Duration(d.conf.Timeouts.DownloadDashSegment) * time.Second,
		}, container, nil
	case "application/vnd.tidal.bts", "vnd.tidal.bt":
		var manifest VNDManifest
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Data.Manifest))
		if err := json.NewDecoder(dec).Decode(&manifest); nil != err {
			logger.Error().Err(err).Str("manifest", respBody.Data.Manifest).Msg("Failed to decode vnd.tidal.bt manifest")
			return nil, types.ContainerInfo{}, fmt.Errorf("decode vnd.tidal.bt manifest: %v", err)
		}

		switch manifest.EncryptionType {
		case "NONE":
		default:
			return nil, types.ContainerInfo{}, fmt.Errorf(
				"encrypted vnd.tidal.bt manifest is not yet implemented: %s",
				manifest.EncryptionType,
			)
		}

		if len(manifest.URLs) == 0 {
			return nil, types.ContainerInfo{}, errors.New("empty vnd.tidal.bt manifest URLs")
		}

		container, err := types.ResolveContainer(manifest.MimeType, manifest.Codec)
		if nil != err {
			logger.
				Error().
				Err(err).
				Str("mime_type", manifest.MimeType).
				Str("codec", manifest.Codec).
				Msg("Failed to infer track extension")

			return nil, types.ContainerInfo{}, fmt.Errorf("infer track extension: %v", err)
		}

		return &VndTrackStream{
			URL:                      manifest.URLs[0],
			DownloadTimeout:          time.Duration(d.conf.Timeouts.DownloadVNDSegment) * time.Second,
			GetTrackFileSizeTimeout:  time.Duration(d.conf.Timeouts.GetVNDTrackFileSize) * time.Second,
			VNDTrackPartsConcurrency: d.conf.Concurrency.VNDTrackParts,
		}, container, nil
	default:
		return nil, types.ContainerInfo{}, fmt.Errorf("unexpected manifest mime type: %s", mimeType)
	}
}
