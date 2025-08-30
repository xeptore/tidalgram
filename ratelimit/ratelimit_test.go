package ratelimit_test

import (
	"testing"

	"github.com/xeptore/tidalgram/ratelimit"
)

func TestTrackDownloadSleepMS(t *testing.T) {
	t.Parallel()
	for range 100_000 {
		ms := ratelimit.TrackDownloadSleepMS().Milliseconds()
		if ms < 1000 || ms > 3000 {
			t.Errorf("expected 1000 <= ms <= 3000, got %d", ms)
		}
	}
}
