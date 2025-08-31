package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/mathutil"
	"github.com/xeptore/tidalgram/tidal/auth"
)

type VndTrackStream struct {
	URL                      string
	DownloadTimeout          time.Duration
	GetTrackFileSizeTimeout  time.Duration
	VNDTrackPartsConcurrency int
}

func (v *VndTrackStream) saveTo(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	fileName string,
) (err error) {
	fileSize, err := v.fileSize(ctx, logger, accessToken)
	if nil != err {
		return fmt.Errorf("unexpected error while getting track file size: %w", err)
	}

	wg, wgctx := errgroup.WithContext(ctx)
	wg.SetLimit(v.VNDTrackPartsConcurrency)

	numChunks := mathutil.DivCeil(fileSize, singlePartChunkSize)
	for i := range numChunks {
		wg.Go(func() (err error) {
			select {
			case <-wgctx.Done():
				return nil
			default:
			}

			logger := logger.With().Int("chunk_index", i).Logger()

			start := i * singlePartChunkSize
			end := min((i+1)*singlePartChunkSize-1, fileSize)

			chunkFileName := fileName + ".chunk." + strconv.Itoa(i)
			f, err := os.OpenFile(chunkFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC, 0o0600)
			if nil != err {
				logger.Error().Err(err).Msg("Failed to create track part file")
				return fmt.Errorf("create track part file: %v", err)
			}
			defer func() {
				if nil != err {
					if removeErr := os.Remove(chunkFileName); nil != removeErr {
						if !errors.Is(removeErr, os.ErrNotExist) {
							logger.Error().Err(removeErr).Msg("Failed to remove incomplete track chunk file")
							err = errors.Join(err, fmt.Errorf("remove incomplete track chunk file: %v", removeErr))
						}
					}
				} else if closeErr := f.Close(); nil != closeErr {
					logger.Error().Err(closeErr).Msg("Failed to close track chunk file")
					err = fmt.Errorf("close track chunk file: %v", closeErr)
				}
			}()

			if err := v.downloadChunkRange(wgctx, logger, accessToken, start, end, f); nil != err {
				return fmt.Errorf("download track chunk %d: %w", i, err)
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("wait for track download workers: %w", err)
	}

	f, err := os.OpenFile(fileName, os.O_CREATE|os.O_SYNC|os.O_TRUNC|os.O_WRONLY, 0o0600)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create track file")
		return fmt.Errorf("create track file: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(fileName); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					logger.Error().Err(removeErr).Msg("Failed to remove incomplete track file")
					err = errors.Join(err, fmt.Errorf("remove incomplete track file: %v", removeErr))
				}
			}
		} else if closeErr := f.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close track file")
			err = fmt.Errorf("close track file: %v", closeErr)
		}
	}()

	for i := range numChunks {
		chunkFileName := fileName + ".chunk." + strconv.Itoa(i)
		if err := writeChunkToTrackFile(f, logger, chunkFileName); nil != err {
			return fmt.Errorf("write track chunk %d to file: %v", i, err)
		}
	}

	if err := f.Sync(); nil != err {
		logger.Error().Err(err).Msg("Failed to sync track file")
		return fmt.Errorf("sync track file: %v", err)
	}

	return nil
}

func (v *VndTrackStream) fileSize(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
) (size int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, v.URL, nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track metadata request")
		return 0, fmt.Errorf("create get track metada request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: v.GetTrackFileSizeTimeout} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track file size request")
		return 0, fmt.Errorf("send get track file size request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track metadata response body")
			err = errors.Join(err, fmt.Errorf("close get track metadata response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
		contentLengthHdr := resp.Header.Get("Content-Length")
		size, err := strconv.Atoi(contentLengthHdr)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to parse 200 response content length header to int")
			err = fmt.Errorf("parse 200 response content length header %q to int: %w", contentLengthHdr, err)

			respBytes, readErr := io.ReadAll(resp.Body)
			if nil != readErr {
				logger.Error().Err(readErr).Msg("Failed to read 200 response body")
				return 0, errors.Join(err, fmt.Errorf("read 200 response body: %w", readErr))
			}

			logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 200 response")

			return 0, errors.Join(
				err,
				fmt.Errorf(
					"failed to parse 200 response content length header %q to int with response body: %s",
					contentLengthHdr,
					string(respBytes),
				),
			)
		}

		return size, nil
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return 0, fmt.Errorf("read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return 0, fmt.Errorf("check if 401 response is token expired: %v", err)
		} else if ok {
			return 0, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return 0, fmt.Errorf("check if 401 response is token invalid: %v", err)
		} else if ok {
			return 0, auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return 0, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return 0, ErrTooManyRequests
	case http.StatusForbidden:
		respBody, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return 0, fmt.Errorf("read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBody); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBody).Msg("Failed to check if 403 response is too many requests")
			return 0, fmt.Errorf("check if 403 response is too many requests: %v", err)
		} else if ok {
			return 0, ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBody).Msg("Unexpected 403 response")

		return 0, fmt.Errorf("unexpected 403 response with body: %s", string(respBody))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return 0, fmt.Errorf("read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return 0, fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}
}

type VNDManifest struct {
	MimeType       string   `json:"mimeType"`
	Codec          string   `json:"codecs"`
	KeyID          *string  `json:"keyId"`
	EncryptionType string   `json:"encryptionType"`
	URLs           []string `json:"urls"`
}

func (v *VndTrackStream) downloadChunkRange(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	start, end int,
	f *os.File,
) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.URL, nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track chunk request")
		return fmt.Errorf("create get track chunk request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	client := http.Client{Timeout: v.DownloadTimeout} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send track chunk download request")
		return fmt.Errorf("send track chunk download request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track chunk response body")
			err = errors.Join(err, fmt.Errorf("close get track chunk response body: %w", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusPartialContent:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return fmt.Errorf("read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return fmt.Errorf("check if 401 response is token expired: %v", err)
		} else if ok {
			return auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return fmt.Errorf("check if 401 response is token invalid: %v", err)
		} else if ok {
			return auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return ErrTooManyRequests
	case http.StatusForbidden:
		respBody, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return fmt.Errorf("read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBody); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBody).Msg("Failed to check if 403 response is too many requests")
			return fmt.Errorf("check if 403 response is too many requests: %v", err)
		} else if ok {
			return ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBody).Msg("Unexpected 403 response")

		return fmt.Errorf("unexpected 403 response with body: %s", string(respBody))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return fmt.Errorf("read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	if n, err := io.Copy(f, resp.Body); nil != err {
		logger.Error().Err(err).Msg("Failed to write track chunk response body to file")
		return fmt.Errorf("write track chunk response body to file: %w", err)
	} else if n == 0 {
		return errors.New("empty track chunk")
	}

	return nil
}
