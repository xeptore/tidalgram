package log

import (
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/constants"
)

func FromConfig(conf config.Log) zerolog.Logger {
	level, err := zerolog.ParseLevel(conf.Level)
	if nil != err {
		panic("invalid logging level: " + conf.Level)
	}

	switch strings.ToLower(conf.Format) {
	case "json":
		return zerolog.
			New(os.Stderr).
			Hook(&stackHook{}).
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
			Hook(&stackHook{}).
			With().
			Timestamp().
			Str("version", constants.Version).
			Str("compile_time", constants.CompileTime).
			Logger().
			Level(level)
	default:
		panic("invalid logging format: " + conf.Format)
	}
}

func NewDefault() zerolog.Logger {
	return zerolog.
		New(zerolog.ConsoleWriter{ //nolint:exhaustruct
			Out:          os.Stderr,
			TimeFormat:   time.RFC3339,
			TimeLocation: time.UTC,
		}).
		Hook(&stackHook{}).
		With().
		Timestamp().
		Str("version", constants.Version).
		Str("compile_time", constants.CompileTime).
		Logger().
		Level(zerolog.InfoLevel)
}
