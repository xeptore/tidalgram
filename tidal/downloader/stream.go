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
	accessToken string,
	id string,
) (s Stream, ext string, quality string, err error) {
	trackURL := fmt.Sprintf(trackStreamAPIFormat, id)
	reqURL, err := url.Parse(trackURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse track URL to build track stream URLs")
		return nil, "", "", fmt.Errorf("parse track URL to build track stream URLs: %v", err)
	}

	params := make(url.Values, 5)
	params.Add("adaptive", "false")
	params.Add("formats", "HEAACV1")
	params.Add("formats", "AACLC")
	params.Add("formats", "FLAC")
	params.Add("formats", "FLAC_HIRES")
	params.Add("manifestType", "MPEG_DASH")
	params.Add("uriScheme", "DATA")
	params.Add("usage", "PLAYBACK")

	reqURL.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track stream URLs request")
		return nil, "", "", fmt.Errorf("create get track stream URLs request: %v", err)
	}

	req.Header.Add("Accept", "application/vnd.api+json")
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetStreamURLs) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track stream URLs request")
		return nil, "", "", fmt.Errorf("send get stream URLs request: %w", err)
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
			return nil, "", "", fmt.Errorf("read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return nil, "", "", fmt.Errorf("check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, "", "", auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return nil, "", "", fmt.Errorf("check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, "", "", auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return nil, "", "", fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, "", "", ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return nil, "", "", fmt.Errorf("read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return nil, "", "", fmt.Errorf("check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, "", "", ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return nil, "", "", fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return nil, "", "", fmt.Errorf("read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, "", "", fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, "", "", fmt.Errorf("read 200 response body: %w", err)
	}

	var respBody struct {
		Data struct {
			Attributes struct {
				URI     string   `json:"uri"`
				Formats []string `json:"formats"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
		return nil, "", "", fmt.Errorf("decode 200 response body: %w", err)
	}

	manifestReader, err := decodeTrackManifestURI(respBody.Data.Attributes.URI)
	if nil != err {
		logger.Error().Err(err).Str("uri", respBody.Data.Attributes.URI).Msg("Failed to decode track manifest URI")
		return nil, "", "", fmt.Errorf("decode track manifest URI: %v", err)
	}

	info, err := mpd.ParseStreamInfo(manifestReader)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse stream info")
		return nil, "", "", fmt.Errorf("parse stream info: %v", err)
	}

	ext, err = types.InferTrackExt(info.MimeType, info.Codec)
	if nil != err {
		logger.
			Error().
			Err(err).
			Str("mime_type", info.MimeType).
			Str("codec", info.Codec).
			Msg("Failed to infer track extension")

		return nil, "", "", fmt.Errorf("infer track extension: %v", err)
	}

	quality = trackManifestQuality(respBody.Data.Attributes.Formats, info)

	return &DashTrackStream{
		Info:            *info,
		DownloadTimeout: time.Duration(d.conf.Timeouts.DownloadDashSegment) * time.Second,
	}, ext, quality, nil
}

const trackManifestDataURIPrefix = "data:application/dash+xml;base64,"

func decodeTrackManifestURI(uri string) (io.Reader, error) {
	if !strings.HasPrefix(uri, trackManifestDataURIPrefix) {
		return nil, fmt.Errorf("unexpected track manifest URI scheme: %s", uri)
	}

	b64 := strings.TrimPrefix(uri, trackManifestDataURIPrefix)
	if len(b64) == 0 {
		return nil, errors.New("empty track manifest data URI payload")
	}

	return base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64)), nil
}

func trackManifestQuality(formats []string, info *mpd.StreamInfo) string {
	var sampleRate *int
	if info.SampleRate > 0 {
		sr := info.SampleRate
		sampleRate = &sr
	}

	var bitDepth *int
	for _, format := range formats {
		switch format {
		case "FLAC_HIRES":
			bd := 24
			bitDepth = &bd
		case "FLAC":
			if nil == bitDepth {
				bd := 16
				bitDepth = &bd
			}
		}
	}

	return types.FormatTrackQuality(info.Codec, bitDepth, sampleRate)
}
