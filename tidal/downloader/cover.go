package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
)

func (d *Downloader) getCover(ctx context.Context, accessToken, coverID string) (b []byte, err error) {
	cachedCoverBytes, err := d.cache.Covers.Fetch(
		coverID,
		cache.DefaultDownloadedCoverTTL,
		func() ([]byte, error) { return d.downloadCover(ctx, accessToken, coverID) },
	)
	if nil != err {
		return nil, err
	}

	return cachedCoverBytes.Value(), nil
}

func (d *Downloader) downloadCover(ctx context.Context, accessToken, coverID string) (b []byte, err error) {
	coverURL, err := url.JoinPath(
		fmt.Sprintf(coverURLFormat, strings.ReplaceAll(coverID, "-", "/")),
	)
	if nil != err {
		return nil, fmt.Errorf("failed to join cover base URL with cover filepath: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get cover request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.DownloadCover) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		return nil, fmt.Errorf("failed to send get track cover request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close get track cover response body: %v", closeErr))
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
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		return nil, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 403 response body: %v", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, ErrTooManyRequests
		}

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read response body: %v", err)
		}

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read 200 response body: %w", err)
	}

	return respBytes, nil
}
