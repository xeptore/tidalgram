package ratelimit

import (
	"math/rand/v2"
	"time"
)

const (
	AlbumDownloadConcurrency          = 3
	PlaylistDownloadConcurrency       = 3
	MixDownloadConcurrency            = 3
	MultipartTrackDownloadConcurrency = 5
	BatchUploadConcurrency            = 8
)

func TrackDownloadSleepMS() time.Duration {
	const (
		from = 2
		to   = 6
	)
	millis := (rand.IntN(to-from)+from)*1000 + rand.N(1000) //nolint:gosec

	return time.Duration(millis) * time.Millisecond
}
