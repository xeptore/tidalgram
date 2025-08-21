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
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/mathutil"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
)

type VndTrackStream struct {
	URL                     string
	DownloadTimeout         time.Duration
	GetTrackFileSizeTimeout time.Duration
}

func (d *VndTrackStream) saveTo(ctx context.Context, accessToken string, fileName string) (err error) {
	fileSize, err := d.fileSize(ctx, accessToken)
	if nil != err {
		return err
	}

	wg, wgCtx := errgroup.WithContext(ctx)
	wg.SetLimit(ratelimit.MultipartTrackDownloadConcurrency)

	numBatches := mathutil.DivCeil(fileSize, singlePartChunkSize)
	for i := range numBatches {
		wg.Go(func() (err error) {
			start := i * singlePartChunkSize
			end := min((i+1)*singlePartChunkSize-1, fileSize)

			partFileName := fileName + ".part." + strconv.Itoa(i)

			f, err := os.OpenFile(
				partFileName,
				os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC,
				0o0600,
			)
			if nil != err {
				return fmt.Errorf("failed to create track part file: %v", err)
			}
			defer func() {
				if nil != err {
					if removeErr := os.Remove(partFileName); nil != removeErr {
						err = errors.Join(err, fmt.Errorf("failed to remove incomplete track part file: %v", removeErr))
					}
				}

				if closeErr := f.Close(); nil != closeErr {
					err = errors.Join(err, fmt.Errorf("failed to close track part file: %v", closeErr))
				}
			}()

			if err := d.downloadRange(wgCtx, accessToken, start, end, f); nil != err {
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
				err = errors.Join(err, fmt.Errorf("failed to remove incomplete track file: %v", removeErr))
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

func (d *VndTrackStream) fileSize(ctx context.Context, accessToken string) (size int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, d.URL, nil)
	if nil != err {
		return 0, fmt.Errorf("failed to create get track metada request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: d.GetTrackFileSizeTimeout} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, context.DeadlineExceeded
		}

		return 0, fmt.Errorf("failed to send get track metadata request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close get track metadata response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
		size, err := strconv.Atoi(resp.Header.Get("Content-Length"))
		if nil != err {
			_, err := io.ReadAll(resp.Body)
			if nil != err {
				return 0, err
			}

			return 0, fmt.Errorf("failed to parse content length: %v", err)
		}

		return size, nil
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return 0, err
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return 0, err
		} else if ok {
			return 0, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return 0, err
		} else if ok {
			return 0, auth.ErrUnauthorized
		}

		return 0, errors.New("received 401 response")
	case http.StatusTooManyRequests:
		return 0, ErrTooManyRequests
	case http.StatusForbidden:
		respBody, err := io.ReadAll(resp.Body)
		if nil != err {
			return 0, err
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBody); nil != err {
			return 0, err
		} else if ok {
			return 0, ErrTooManyRequests
		}

		return 0, errors.New("unexpected 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return 0, err
		}

		return 0, fmt.Errorf("unexpected status code: %d", code)
	}
}

type VNDManifest struct {
	MimeType       string   `json:"mimeType"`
	Codec          string   `json:"codecs"`
	KeyID          *string  `json:"keyId"`
	EncryptionType string   `json:"encryptionType"`
	URLs           []string `json:"urls"`
}

func (d *VndTrackStream) downloadRange(ctx context.Context, accessToken string, start, end int, f *os.File) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if nil != err {
		return fmt.Errorf("failed to create get track part request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", start, end))

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
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track part response body: %v", closeErr),
			)
		}
	}()

	switch status := resp.StatusCode; status {
	case http.StatusPartialContent:
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
		respBody, err := io.ReadAll(resp.Body)
		if nil != err {
			return err
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBody); nil != err {
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

	respBody, err := io.ReadAll(resp.Body)
	if nil != err {
		return err
	}

	if n, err := io.Copy(f, bytes.NewReader(respBody)); nil != err {
		return fmt.Errorf("failed to write track part to file: %v", err)
	} else if n == 0 {
		return errors.New("empty track part")
	}

	return nil
}
