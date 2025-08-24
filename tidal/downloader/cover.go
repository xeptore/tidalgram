package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/unit"
)

func (d *Downloader) getCover(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	coverID string,
) (b []byte, err error) {
	cachedCover, err := d.cache.Covers.Fetch(
		coverID,
		cache.DefaultDownloadedCoverTTL,
		func() ([]byte, error) { return d.downloadCover(ctx, logger, accessToken, coverID) },
	)
	if nil != err {
		return nil, fmt.Errorf("failed to download cover: %w", err)
	}

	return cachedCover.Value(), nil
}

func (d *Downloader) downloadCover(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	coverID string,
) ([]byte, error) {
	coverURL, err := url.JoinPath(
		fmt.Sprintf(coverURLFormat, strings.ReplaceAll(coverID, "-", "/")),
	)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join cover base URL with cover filepath")
		return nil, fmt.Errorf("failed to join cover base URL with cover filepath: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get cover request")
		return nil, fmt.Errorf("failed to create get cover request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.DownloadCover) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send download cover request")
		return nil, fmt.Errorf("failed to send download cover request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track cover response body")
			err = errors.Join(err, fmt.Errorf("failed to close get track cover response body: %v", closeErr))
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

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read response body")
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	compressed, err := compressImage(ctx, logger, respBytes, 200*unit.Kibibyte)
	if nil != err {
		return nil, fmt.Errorf("failed to compress image: %w", err)
	}

	return compressed, nil
}

func compressImage(ctx context.Context, logger zerolog.Logger, b []byte, maxSize int64) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	image := bytes.Clone(b)

	for quantizer := 1; quantizer < 32; quantizer++ {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		r, w, err := os.Pipe()
		if nil != err {
			logger.Error().Err(err).Msg("Failed to create pipe for ffmpeg command")
			return nil, fmt.Errorf("failed to create pipe for ffmpeg command: %v", err)
		}

		cmd := exec.CommandContext(
			ctx,
			"ffmpeg",
			"-i",
			"-",
			"-q:v",
			strconv.Itoa(quantizer),
			"-vf",
			"scale=iw:ih",
			"-frames:v",
			"1",
			"-f",
			"mjpeg",
			"-",
		)
		logger.Debug().Strs("args", cmd.Args).Msg("Starting ffmpeg command")

		// Setpgid is required to kill the process group when the context is cancelled.
		// Only works on Unix.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} //nolint:exhaustruct

		cmd.Cancel = func() error {
			p := cmd.Process
			if p == nil {
				return nil
			}

			// Send SIGTERM to the process group (-PID) so children get it too.
			_ = syscall.Kill(-p.Pid, syscall.SIGTERM)

			for {
				time.Sleep(1 * time.Second)

				if err := syscall.Kill(cmd.Process.Pid, 0); nil != err {
					return os.ErrProcessDone
				}

				_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
			}
		}

		cmd.Stdin = r

		var stderr bytes.Buffer
		cmd.Stdout = &stderr

		var stdout bytes.Buffer
		cmd.Stdout = &stdout

		if err := cmd.Start(); nil != err {
			logger.Error().Err(err).Msg("Failed to start ffmpeg command")

			if closeErr := w.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe writer")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe writer: %v", closeErr))
			}

			if closeErr := r.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe reader")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe reader: %v", closeErr))
			}

			return nil, fmt.Errorf("failed to start ffmpeg command: %v", err)
		}

		if n, err := bytes.NewReader(image).WriteTo(w); nil != err {
			cancel()
			logger.Error().Err(err).Msg("Failed to write image to ffmpeg stdin")

			if closeErr := w.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe writer")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe writer: %v", closeErr))
			}

			if closeErr := r.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe reader")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe reader: %v", closeErr))
			}

			if waitErr := cmd.Wait(); nil != waitErr {
				logger.
					Error().
					Err(waitErr).
					Bytes("stderr", stderr.Bytes()).
					Msg("Failed to wait for ffmpeg command")
				err = errors.Join(err, fmt.Errorf("failed to wait for ffmpeg command: %v", waitErr))
			}

			return nil, fmt.Errorf("failed to write image to ffmpeg stdin: %v", err)
		} else if expected := int64(len(b)); n != expected {
			cancel()
			logger.Error().Int64("expected", expected).Int64("actual", n).Msg("Failed to write image to ffmpeg stdin")

			var err error

			if closeErr := w.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe writer")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe writer: %v", closeErr))
			}

			if closeErr := r.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe reader")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe reader: %v", closeErr))
			}

			if waitErr := cmd.Wait(); nil != waitErr {
				logger.
					Error().
					Err(waitErr).
					Bytes("stderr", stderr.Bytes()).
					Msg("Failed to wait for ffmpeg command")
				err = errors.Join(err, fmt.Errorf("failed to wait for ffmpeg command: %v", waitErr))
			}

			return nil, errors.Join(err, fmt.Errorf("failed to write image to ffmpeg stdin: %v", io.ErrShortWrite))
		}

		if err := w.Close(); nil != err {
			cancel()
			logger.Error().Err(err).Msg("Failed to close ffmpeg pipe writer")

			if closeErr := r.Close(); nil != closeErr {
				logger.Error().Err(err).Msg("Failed to close ffmpeg pipe reader")
				err = errors.Join(err, fmt.Errorf("failed to close ffmpeg pipe reader: %v", closeErr))
			}

			if waitErr := cmd.Wait(); nil != waitErr {
				logger.
					Error().
					Err(waitErr).
					Bytes("stderr", stderr.Bytes()).
					Msg("Failed to wait for ffmpeg command")
				err = errors.Join(err, fmt.Errorf("failed to wait for ffmpeg command: %v", waitErr))
			}

			return nil, fmt.Errorf("failed to close ffmpeg pipe writer: %v", err)
		}

		if err := r.Close(); nil != err {
			cancel()
			logger.Error().Err(err).Msg("Failed to close ffmpeg pipe reader")

			if waitErr := cmd.Wait(); nil != waitErr {
				logger.
					Error().
					Err(waitErr).
					Bytes("stderr", stderr.Bytes()).
					Msg("Failed to wait for ffmpeg command")
				err = errors.Join(err, fmt.Errorf("failed to wait for ffmpeg command: %v", waitErr))
			}

			return nil, fmt.Errorf("failed to close ffmpeg pipe reader: %v", err)
		}

		if err := cmd.Wait(); nil != err {
			cancel()
			logger.
				Error().
				Err(err).
				Bytes("stderr", stderr.Bytes()).
				Msg("Failed to wait for ffmpeg command")

			return nil, fmt.Errorf("failed to wait for ffmpeg command: %v", err)
		}

		result := stdout.Bytes()
		if len(result) <= int(maxSize) {
			return result, nil
		}
	}

	return nil, errors.New("failed to compress image after 32 quantizers")
}
