package logging

import (
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/constants"
)

func FromConfig(cfg *config.Logging) zerolog.Logger {
	level, err := zerolog.ParseLevel(cfg.Level)
	if nil != err {
		panic("invalid logging level: " + cfg.Level)
	}

	switch strings.ToLower(cfg.Format) {
	case "json":
		return zerolog.
			New(os.Stderr).
			With().
			Timestamp().
			Str("version", constants.Version).
			Str("compile_time", constants.CompileTime).
			Logger().
			Level(level)
	case "pretty":
		return zerolog.
			New(zerolog.ConsoleWriter{ //nolint:exhaustruct
				Out:          os.Stderr,
				TimeFormat:   time.RFC3339,
				TimeLocation: time.UTC,
			}).
			With().
			Timestamp().
			Str("version", constants.Version).
			Str("compile_time", constants.CompileTime).
			Logger().
			Level(level)
	default:
		panic("invalid logging format: " + cfg.Format)
	}
}

func NewDefault() zerolog.Logger {
	return zerolog.
		New(zerolog.ConsoleWriter{ //nolint:exhaustruct
			Out:          os.Stderr,
			TimeFormat:   time.RFC3339,
			TimeLocation: time.UTC,
		}).
		With().
		Timestamp().
		Str("version", constants.Version).
		Str("compile_time", constants.CompileTime).
		Logger().
		Level(zerolog.InfoLevel)
}
