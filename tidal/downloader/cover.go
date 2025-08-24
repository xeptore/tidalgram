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
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
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

	pixFmt, err := ProbePixelFormat(ctx, logger, respBytes)
	if nil != err {
		return nil, fmt.Errorf("failed to probe pixel format: %v", err)
	}

	switch pixFmt {
	case "yuvj420p":
		b, err := Filter444(ctx, logger, respBytes)
		if nil != err {
			return nil, fmt.Errorf("failed to filter image: %v", err)
		}

		return b, nil
	case "yuvj444p":
		return respBytes, nil
	default:
		logger.Error().Str("pix_fmt", pixFmt).Msg("Unexpected pix_fmt")
		return nil, fmt.Errorf("unexpected pix_fmt: %s", pixFmt)
	}
}

func ProbePixelFormat(ctx context.Context, logger zerolog.Logger, image []byte) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r, w, err := os.Pipe()
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create pipe for ffprobe command")
		return "", fmt.Errorf("failed to create pipe for ffprobe command: %v", err)
	}

	cmd := exec.CommandContext(
		ctx,
		"ffprobe",
		"-v",
		"error",
		"-select_streams",
		"v:0",
		"-show_entries",
		"stream=pix_fmt",
		"-of",
		"csv=p=0",
		"-",
	)
	logger.Debug().Strs("args", cmd.Args).Msg("Starting ffprobe command")

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
		logger.Error().Err(err).Msg("Failed to start ffprobe command")

		if closeErr := w.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe writer: %v", closeErr))
		}

		if closeErr := r.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe reader: %v", closeErr))
		}

		return "", fmt.Errorf("failed to start ffprobe command: %v", err)
	}

	if n, err := bytes.NewReader(image).WriteTo(w); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to write image to ffprobe stdin")

		if closeErr := w.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe writer: %v", closeErr))
		}

		if closeErr := r.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe reader: %v", closeErr))
		}

		if waitErr := cmd.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", stderr.Bytes()).
				Msg("Failed to wait for ffprobe command")
			err = errors.Join(err, fmt.Errorf("failed to wait for ffprobe command: %v", waitErr))
		}

		return "", fmt.Errorf("failed to write image to ffprobe stdin: %v", err)
	} else if expected := int64(len(image)); n != expected {
		cancel()
		logger.Error().Int64("expected", expected).Int64("actual", n).Msg("Failed to write image to ffprobe stdin")

		var err error

		if closeErr := w.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe writer: %v", closeErr))
		}

		if closeErr := r.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe reader: %v", closeErr))
		}

		if waitErr := cmd.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", stderr.Bytes()).
				Msg("Failed to wait for ffprobe command")
			err = errors.Join(err, fmt.Errorf("failed to wait for ffprobe command: %v", waitErr))
		}

		return "", errors.Join(err, fmt.Errorf("failed to write image to ffprobe stdin: %v", io.ErrShortWrite))
	}

	if err := w.Close(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to close ffprobe pipe writer")

		if closeErr := r.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close ffprobe pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close ffprobe pipe reader: %v", closeErr))
		}

		if waitErr := cmd.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", stderr.Bytes()).
				Msg("Failed to wait for ffprobe command")
			err = errors.Join(err, fmt.Errorf("failed to wait for ffprobe command: %v", waitErr))
		}

		return "", fmt.Errorf("failed to close ffprobe pipe writer: %v", err)
	}

	if err := r.Close(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to close ffprobe pipe reader")

		if waitErr := cmd.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", stderr.Bytes()).
				Msg("Failed to wait for ffprobe command")
			err = errors.Join(err, fmt.Errorf("failed to wait for ffprobe command: %v", waitErr))
		}

		return "", fmt.Errorf("failed to close ffprobe pipe reader: %v", err)
	}

	if err := cmd.Wait(); nil != err {
		cancel()
		logger.
			Error().
			Err(err).
			Bytes("stderr", stderr.Bytes()).
			Msg("Failed to wait for ffprobe command")

		return "", fmt.Errorf("failed to wait for ffprobe command: %v", err)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func Filter444(ctx context.Context, logger zerolog.Logger, image []byte) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	djpegR, djpegW, err := os.Pipe()
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create pipe for djpeg command")
		return nil, fmt.Errorf("failed to create pipe for djpeg command: %v", err)
	}

	cjpegR, cjpegW, err := os.Pipe()
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create pipe for cjpeg command")
		return nil, fmt.Errorf("failed to create pipe for cjpeg command: %v", err)
	}

	djpeg := exec.CommandContext(ctx, "djpeg", "-ppm")
	logger.Debug().Strs("args", djpeg.Args).Msg("Starting djpeg command")

	// Setpgid is required to kill the process group when the context is cancelled.
	// Only works on Unix.
	djpeg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} //nolint:exhaustruct

	djpeg.Cancel = func() error {
		p := djpeg.Process
		if p == nil {
			return nil
		}

		// Send SIGTERM to the process group (-PID) so children get it too.
		_ = syscall.Kill(-p.Pid, syscall.SIGTERM)

		for {
			time.Sleep(1 * time.Second)

			if err := syscall.Kill(djpeg.Process.Pid, 0); nil != err {
				return os.ErrProcessDone
			}

			_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		}
	}

	djpeg.Stdin = djpegR
	djpeg.Stdout = cjpegW

	var djpegStderr bytes.Buffer
	djpeg.Stderr = &djpegStderr

	cjpeg := exec.CommandContext(
		ctx,
		"cjpeg",
		"-sample",
		"1x1",
		"-quality",
		"95",
		"-optimize",
		"-progressive",
	)
	logger.Debug().Strs("args", cjpeg.Args).Msg("Starting cjpeg command")

	// Setpgid is required to kill the process group when the context is cancelled.
	// Only works on Unix.
	cjpeg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} //nolint:exhaustruct

	cjpeg.Cancel = func() error {
		p := cjpeg.Process
		if p == nil {
			return nil
		}

		// Send SIGTERM to the process group (-PID) so children get it too.
		_ = syscall.Kill(-p.Pid, syscall.SIGTERM)

		for {
			time.Sleep(1 * time.Second)

			if err := syscall.Kill(cjpeg.Process.Pid, 0); nil != err {
				return os.ErrProcessDone
			}

			_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		}
	}

	cjpeg.Stdin = cjpegR

	var cjpegStdout bytes.Buffer
	cjpeg.Stdout = &cjpegStdout

	var cjpegStderr bytes.Buffer
	cjpeg.Stderr = &cjpegStderr

	// Start consumer first to avoid blocking the producer.
	if err := cjpeg.Start(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to start cjpeg command")

		if closeErr := djpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe writer: %v", closeErr))
		}

		if closeErr := djpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe reader: %v", closeErr))
		}

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		return nil, fmt.Errorf("failed to start cjpeg command: %v", err)
	}

	if err := djpeg.Start(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to start djpeg command")

		if closeErr := djpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe writer: %v", closeErr))
		}

		if closeErr := djpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe reader: %v", closeErr))
		}

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe writer: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to start djpeg command: %v", err)
	}

	if n, err := bytes.NewReader(image).WriteTo(djpegW); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to write image to djpeg stdin")

		if closeErr := djpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe writer: %v", closeErr))
		}

		if closeErr := djpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe reader: %v", closeErr))
		}

		if waitErr := djpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", djpegStderr.Bytes()).
				Msg("Failed to wait for djpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for djpeg command: %v", waitErr))
		}

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe writer: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to write image to djpeg stdin: %v", err)
	} else if expected := int64(len(image)); n != expected {
		cancel()
		logger.
			Error().
			Int64("expected", expected).
			Int64("actual", n).
			Msg("Failed to write image to djpeg stdin")

		var err error

		if closeErr := djpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe writer: %v", closeErr))
		}

		if closeErr := djpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe reader: %v", closeErr))
		}

		if waitErr := djpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", djpegStderr.Bytes()).
				Msg("Failed to wait for djpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for djpeg command: %v", waitErr))
		}

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe writer: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, errors.Join(
			err,
			fmt.Errorf("failed to write image to djpeg stdin: %v", io.ErrShortWrite),
		)
	}

	if err := djpegW.Close(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to close djpeg pipe writer")

		if closeErr := djpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close djpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close djpeg pipe reader: %v", closeErr))
		}

		if waitErr := djpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", djpegStderr.Bytes()).
				Msg("Failed to wait for djpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for djpeg command: %v", waitErr))
		}

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe writer: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to close djpeg pipe: %v", err)
	}

	if err := djpegR.Close(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to close djpeg pipe reader")

		if waitErr := djpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", djpegStderr.Bytes()).
				Msg("Failed to wait for djpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for djpeg command: %v", waitErr))
		}

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe writer: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to close djpeg pipe: %v", err)
	}

	if err := djpeg.Wait(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to wait for djpeg command")

		if closeErr := cjpegW.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe writer: %v", closeErr))
		}

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to wait for djpeg command: %v", err)
	}

	if err := cjpegW.Close(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to close cjpeg pipe writer")

		if closeErr := cjpegR.Close(); nil != closeErr {
			logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")
			err = errors.Join(err, fmt.Errorf("failed to close cjpeg pipe reader: %v", closeErr))
		}

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to close cjpeg pipe: %v", err)
	}

	if err := cjpegR.Close(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to close cjpeg pipe reader")

		if waitErr := cjpeg.Wait(); nil != waitErr {
			logger.
				Error().
				Err(waitErr).
				Bytes("stderr", cjpegStderr.Bytes()).
				Msg("Failed to wait for cjpeg command")
			err = errors.Join(err, fmt.Errorf("failed to wait for cjpeg command: %v", waitErr))
		}

		return nil, fmt.Errorf("failed to close cjpeg pipe: %v", err)
	}

	if err := cjpeg.Wait(); nil != err {
		cancel()
		logger.Error().Err(err).Msg("Failed to wait for cjpeg command")

		return nil, fmt.Errorf("failed to wait for cjpeg command: %v", err)
	}

	return cjpegStdout.Bytes(), nil
}
