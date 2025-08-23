package redact

import (
	"math"
	"strings"
)

func String(s string) string {
	l := len(s)

	var flag int
	if l%4 != 0 {
		flag = 1
	}

	return s[0:int(math.Floor(float64(l)*.25))] +
		strings.Repeat("*", int(math.RoundToEven(float64(l)*.5))+(1&flag)) +
		s[int(math.Floor(float64(l)*.75))+(1&flag):]
}
