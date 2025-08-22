package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/mathutil"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/mpd"
)

type DashTrackStream struct {
	Info            mpd.StreamInfo
	DownloadTimeout time.Duration
}

func (d *DashTrackStream) saveTo(ctx context.Context, accessToken string, fileName string) (err error) {
	var (
		numBatches = mathutil.DivCeil(d.Info.Parts.Count, maxBatchParts)
		wg, wgCtx  = errgroup.WithContext(ctx)
	)

	wg.SetLimit(numBatches)
	for i := range numBatches {
		wg.Go(func() error {
			if err := d.downloadBatch(wgCtx, accessToken, fileName, i); nil != err {
				return fmt.Errorf("failed to download track batch: %w", err)
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("failed to wait for track download workers: %w", err)
	}

	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_SYNC|os.O_TRUNC|os.O_WRONLY, 0o0600)
	if nil != err {
		return fmt.Errorf("failed to create track file: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(fileName); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("failed to remove incomplete track file: %v", removeErr))
				}
			}
		} else if closeErr := f.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close track file: %v", closeErr))
		}
	}()

	for i := range numBatches {
		partFileName := fileName + ".part." + strconv.Itoa(i)
		if err := writePartToTrackFile(f, partFileName); nil != err {
			return fmt.Errorf("failed to write track part to file: %v", err)
		}
	}

	if err := f.Sync(); nil != err {
		return fmt.Errorf("failed to sync track file: %v", err)
	}

	return nil
}

func writePartToTrackFile(f *os.File, partFileName string) (err error) {
	fp, err := os.OpenFile(partFileName, os.O_RDONLY, 0o0600)
	if nil != err {
		return fmt.Errorf("failed to open track part file: %v", err)
	}
	defer func() {
		if closeErr := fp.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close track part file: %v", closeErr))
		}
	}()

	if _, err := io.Copy(f, fp); nil != err {
		return fmt.Errorf("failed to copy track part to track file: %v", err)
	}

	if err := os.Remove(partFileName); nil != err {
		return fmt.Errorf("failed to remove track part file: %v", err)
	}

	return nil
}

func (d *DashTrackStream) downloadBatch(ctx context.Context, accessToken, fileName string, idx int) (err error) {
	f, err := os.OpenFile(
		fileName+".part."+strconv.Itoa(idx),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC,
		0o600,
	)
	if nil != err {
		return fmt.Errorf("failed to create track part file: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(f.Name()); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("failed to remove incomplete track part file: %v", removeErr))
				}
			}
		} else if closeErr := f.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close track part file: %v", closeErr))
		}
	}()

	start := idx * maxBatchParts
	end := min(d.Info.Parts.Count, (idx+1)*maxBatchParts)

	for i := range end - start {
		segmentIdx := start + i
		link := strings.Replace(
			d.Info.Parts.InitializationURLTemplate,
			"$Number$",
			strconv.Itoa(segmentIdx),
			1,
		)
		if err := d.downloadSegment(ctx, accessToken, link, f); nil != err {
			return fmt.Errorf("failed to download track segment: %w", err)
		}
	}

	return nil
}

func (d *DashTrackStream) downloadSegment(ctx context.Context, accessToken, link string, f *os.File) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if nil != err {
		return fmt.Errorf("failed to create get track part request: %w", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: d.DownloadTimeout} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		return fmt.Errorf("failed to send track part download request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close get track part response body: %v", closeErr))
		}
	}()

	switch status := resp.StatusCode; status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return fmt.Errorf("failed to read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return auth.ErrUnauthorized
		}

		return fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return fmt.Errorf("failed to read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return ErrTooManyRequests
		}

		return fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return fmt.Errorf("failed to read response body: %w", err)
		}

		return fmt.Errorf("unexpected response code %d with body: %s", status, string(respBytes))
	}

	if n, err := io.Copy(f, resp.Body); nil != err {
		return fmt.Errorf("failed to write track part to file: %w", err)
	} else if n == 0 {
		return errors.New("empty track part")
	}

	return nil
}
