package ratelimit

import (
	"math/rand/v2"
	"time"
)

func TrackDownloadSleepMS() time.Duration {
	const (
		from = 1
		to   = 3
	)
	millis := (rand.IntN(to-from)+from)*1000 + rand.N(1000) //nolint:gosec

	return time.Duration(millis) * time.Millisecond
}
