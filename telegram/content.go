package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"
)

// TelegramAudioCacheKey identifies a Telegram audio upload by track and the
// SHA-256 digest of the exact bytes uploaded to Telegram.
func TelegramAudioCacheKey(trackID string, digest string) string {
	return "telegram-audio:" + trackID + ":" + digest
}

// TrackMetaCacheKey is the metadata cache key. It is track identity only.
// Audio bytes are not part of it.
func TrackMetaCacheKey(trackID string) string {
	return "track:" + trackID + ":metadata"
}

// FileSHA256 hashes the file at path without buffering it in memory.
// The digest is the content identity of the bytes later uploaded to Telegram.
func FileSHA256(ctx context.Context, logger zerolog.Logger, path string) (digest string, err error) {
	file, err := os.Open(path)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to open track audio file")
		return "", fmt.Errorf("open audio file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close track audio file")
			err = errors.Join(err, fmt.Errorf("close audio file: %w", closeErr))
		}
	}()

	sum := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		if err = ctx.Err(); nil != err {
			return "", fmt.Errorf("hash audio file: %w", err)
		}

		n, readErr := file.Read(buf)
		if n > 0 {
			if _, writeErr := sum.Write(buf[:n]); nil != writeErr {
				logger.Error().Err(writeErr).Msg("Failed to hash track audio file")
				return "", fmt.Errorf("hash audio file: %w", writeErr)
			}
		}
		if nil == readErr {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if !errors.Is(readErr, context.Canceled) && !errors.Is(readErr, context.DeadlineExceeded) {
			logger.Error().Err(readErr).Msg("Failed to hash track audio file")
		}

		return "", fmt.Errorf("hash audio file: %w", readErr)
	}

	return hex.EncodeToString(sum.Sum(nil)), nil
}
