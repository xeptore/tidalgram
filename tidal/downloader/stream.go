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

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/mpd"
	"github.com/xeptore/tidalgram/tidal/types"
)

type Stream interface {
	saveTo(ctx context.Context, accessToken string, fileName string) error
}

func (d *Downloader) getStream(ctx context.Context, accessToken, id string) (s Stream, f *types.TrackFormat, err error) {
	trackURL := fmt.Sprintf(trackStreamAPIFormat, id)

	reqURL, err := url.Parse(trackURL)
	if nil != err {
		return nil, nil, fmt.Errorf("failed to parse track URL to build track stream URLs: %v", err)
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
		return nil, nil, fmt.Errorf("failed to create get track stream URLs request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetStreamURLs) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, context.DeadlineExceeded
		}

		return nil, nil, fmt.Errorf("failed to send get track stream URLs request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track stream URLs response body: %v", closeErr),
			)
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, nil, err
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, nil, err
		} else if ok {
			return nil, nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, nil, err
		} else if ok {
			return nil, nil, auth.ErrUnauthorized
		}

		return nil, nil, errors.New("received 401 response")
	case http.StatusTooManyRequests:
		return nil, nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, nil, err
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return nil, nil, err
		} else if ok {
			return nil, nil, ErrTooManyRequests
		}

		return nil, nil, errors.New("unexpected 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, nil, err
		}

		return nil, nil, fmt.Errorf(
			"unexpected status code received from get track stream URLs: %d",
			code,
		)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, nil, err
	}
	var respBody struct {
		ManifestMimeType string `json:"manifestMimeType"`
		Manifest         string `json:"manifest"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, nil, fmt.Errorf("failed to decode track stream response body: %v", err)
	}

	switch mimeType := respBody.ManifestMimeType; mimeType {
	case "application/dash+xml", "dash+xml":
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Manifest))
		info, err := mpd.ParseStreamInfo(dec)
		if nil != err {
			return nil, nil, fmt.Errorf("failed to parse stream info: %v", err)
		}

		if _, err := types.InferTrackExt(info.MimeType, info.Codec); nil != err {
			return nil, nil, err
		}
		format := types.TrackFormat{MimeType: info.MimeType, Codec: info.Codec}

		return &DashTrackStream{
			Info:            *info,
			DownloadTimeout: time.Duration(d.conf.Timeouts.DownloadDashSegment) * time.Second,
		}, &format, nil
	case "application/vnd.tidal.bts", "vnd.tidal.bt":
		var manifest VNDManifest
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Manifest))
		if err := json.NewDecoder(dec).Decode(&manifest); nil != err {
			return nil, nil, fmt.Errorf("failed to decode vnd.tidal.bt manifest: %v", err)
		}

		switch manifest.EncryptionType {
		case "NONE":
		default:
			return nil, nil, fmt.Errorf(
				"encrypted vnd.tidal.bt manifest is not yet implemented: %s",
				manifest.EncryptionType,
			)
		}

		if _, err := types.InferTrackExt(manifest.MimeType, manifest.Codec); nil != err {
			return nil, nil, err
		}
		format := &types.TrackFormat{MimeType: manifest.MimeType, Codec: manifest.Codec}

		if len(manifest.URLs) == 0 {
			return nil, nil, errors.New("empty vnd.tidal.bt manifest URLs")
		}

		return &VndTrackStream{
			URL:                     manifest.URLs[0],
			DownloadTimeout:         time.Duration(d.conf.Timeouts.DownloadVNDSegment) * time.Second,
			GetTrackFileSizeTimeout: time.Duration(d.conf.Timeouts.GetVNDTrackFileSize) * time.Second,
		}, format, nil
	default:
		return nil, nil, fmt.Errorf("unexpected manifest mime type: %s", mimeType)
	}
}
