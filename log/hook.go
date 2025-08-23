package log

import (
	"runtime"

	"github.com/rs/zerolog"
)

type stackHook struct{}

func (h *stackHook) Run(e *zerolog.Event, level zerolog.Level, message string) {
	if level < zerolog.ErrorLevel {
		return
	}

	st := traces(5)
	arr := zerolog.Arr()
	for _, s := range st {
		arr.Dict(zerolog.Dict().
			Int("line", s.Line).
			Str("file", s.File).
			Str("function", s.Function),
		)
	}
	e.Array("stack", arr)
}

type stackTrace struct {
	// Line is the file line number of the location in this frame.
	// For non-leaf frames, this will be the location of a call.
	// This may be zero, if not known.
	Line int
	// File is the file name of the location in this frame.
	// For non-leaf frames, this will be the location of a call.
	// This may be the empty string if not known.
	File string
	// Function is the package path-qualified function name of
	// this call frame. If non-empty, this string uniquely
	// identifies a single function in the program.
	// This may be the empty string if not known.
	Function string
}

func traces(skip int) []stackTrace {
	const depth = 64
	var pcs [depth]uintptr
	n := runtime.Callers(skip, pcs[:])
	if n == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs[:n])
	st := make([]stackTrace, 0, n)
	for {
		frame, ok := frames.Next()
		st = append(st, stackTrace{
			Line:     frame.Line,
			File:     frame.File,
			Function: frame.Function,
		})
		if !ok {
			break
		}
	}

	return st
}
