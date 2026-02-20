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

	"github.com/rs/zerolog"
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

func (d *DashTrackStream) saveTo(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	fileName string,
) (err error) {
	var (
		numChunks = mathutil.DivCeil(d.Info.Parts.Count, maxChunkParts)
		wg, wgctx = errgroup.WithContext(ctx)
	)

	wg.SetLimit(numChunks)
	for i := range numChunks {
		wg.Go(func() error {
			select {
			case <-wgctx.Done():
				return nil
			default:
			}

			logger := logger.With().Int("chunk_index", i).Logger()

			if err := d.downloadChunk(wgctx, logger, accessToken, fileName, i); nil != err {
				return fmt.Errorf("download track chunk: %w", err)
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
			err = errors.Join(err, fmt.Errorf("close track file: %v", closeErr))
		}
	}()

	for i := range numChunks {
		chunkFileName := fileName + ".chunk." + strconv.Itoa(i)
		if err := writeChunkToTrackFile(f, logger, chunkFileName); nil != err {
			return fmt.Errorf("write track chunk to file: %v", err)
		}
	}

	if err := f.Sync(); nil != err {
		logger.Error().Err(err).Msg("Failed to sync track file")
		return fmt.Errorf("sync track file: %v", err)
	}

	return nil
}

func writeChunkToTrackFile(f *os.File, logger zerolog.Logger, chunkFileName string) (err error) {
	fp, err := os.OpenFile(chunkFileName, os.O_RDONLY, 0o0600)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to open track chunk file")
		return fmt.Errorf("open track chunk file: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(chunkFileName); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					logger.Error().Err(removeErr).Msg("Failed to remove incomplete track chunk file")
					err = errors.Join(err, fmt.Errorf("remove incomplete track chunk file: %v", removeErr))
				}
			}
		} else if closeErr := fp.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close track chunk file")
			err = errors.Join(err, fmt.Errorf("close track chunk file: %v", closeErr))
		}
	}()

	if _, err := io.Copy(f, fp); nil != err {
		logger.Error().Err(err).Msg("Failed to copy track chunk to track file")
		return fmt.Errorf("copy track chunk to track file: %v", err)
	}

	if err := os.Remove(chunkFileName); nil != err {
		logger.Error().Err(err).Msg("Failed to remove track chunk file")
		return fmt.Errorf("remove track chunk file: %v", err)
	}

	return nil
}

func (d *DashTrackStream) downloadChunk(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	fileName string,
	idx int,
) (err error) {
	f, err := os.OpenFile(
		fileName+".chunk."+strconv.Itoa(idx),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_SYNC,
		0o600,
	)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create track chunk file")
		return fmt.Errorf("create track chunk file: %v", err)
	}
	defer func() {
		if nil != err {
			if removeErr := os.Remove(f.Name()); nil != removeErr { //nolint:gosec
				if !errors.Is(removeErr, os.ErrNotExist) {
					logger.Error().Err(removeErr).Msg("Failed to remove incomplete track chunk file")
					err = errors.Join(err, fmt.Errorf("remove incomplete track chunk file: %v", removeErr))
				}
			}
		} else if closeErr := f.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close track chunk file")
			err = errors.Join(err, fmt.Errorf("close track chunk file: %v", closeErr))
		}
	}()

	start := idx * maxChunkParts
	end := min(d.Info.Parts.Count, (idx+1)*maxChunkParts)

	for i := start; i < end; i++ {
		link := strings.Replace(
			d.Info.Parts.InitializationURLTemplate,
			"$Number$",
			strconv.Itoa(i),
			1,
		)
		if err := d.downloadSegment(ctx, logger, accessToken, link, f); nil != err {
			return fmt.Errorf("download track segment: %w", err)
		}
	}

	return nil
}

func (d *DashTrackStream) downloadSegment(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	link string,
	f *os.File,
) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track segment request")
		return fmt.Errorf("create get track segment request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: d.DownloadTimeout} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send track segment download request")
		return fmt.Errorf("send track segment download request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track segment response body")
			err = errors.Join(err, fmt.Errorf("close get track segment response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
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
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return fmt.Errorf("read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return fmt.Errorf("check if 403 response is too many requests: %v", err)
		} else if ok {
			return ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
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
		logger.Error().Err(err).Msg("Failed to write track segment to file")
		return fmt.Errorf("write track segment to file: %w", err)
	} else if n == 0 {
		return errors.New("empty track segment")
	}

	return nil
}
