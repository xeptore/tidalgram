package downloader

import (
	"bytes"
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
				return err
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return err
	}

	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_SYNC|os.O_TRUNC|os.O_WRONLY, 0o0600)
	if nil != err {
		return fmt.Errorf("failed to create track file: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(fileName); nil != removeErr {
				err = fmt.Errorf("failed to remove incomplete track file: %v: %w", removeErr, err)
			}
		}

		if closeErr := f.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close track file: %v", closeErr))
		}
	}()

	for i := range numBatches {
		partFileName := fileName + ".part." + strconv.Itoa(i)

		if err := writePartToTrackFile(f, partFileName); nil != err {
			return err
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
				err = errors.Join(err, fmt.Errorf("failed to remove incomplete track part file: %v", removeErr))
			}
		}

		if closeErr := f.Close(); nil != closeErr {
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
			return err
		}
	}

	return nil
}

func (d *DashTrackStream) downloadSegment(ctx context.Context, accessToken, link string, f *os.File) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if nil != err {
		return fmt.Errorf("failed to create get track part request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: d.DownloadTimeout} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		return fmt.Errorf("failed to send get track part request: %v", err)
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
			return err
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return err
		} else if ok {
			return auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return err
		} else if ok {
			return auth.ErrUnauthorized
		}

		return errors.New("received 401 response")
	case http.StatusTooManyRequests:
		return ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return err
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return err
		} else if ok {
			return ErrTooManyRequests
		}

		return errors.New("unexpected 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return err
		}

		return fmt.Errorf("unexpected status code received from get track part: %d", status)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return err
	}
	if n, err := io.Copy(f, bytes.NewReader(respBytes)); nil != err {
		return fmt.Errorf("failed to write track part to file: %v", err)
	} else if n == 0 {
		return errors.New("empty track part")
	}

	return nil
}
