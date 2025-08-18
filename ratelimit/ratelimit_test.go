package ratelimit_test

import (
	"testing"

	"github.com/xeptore/tidalgram/ratelimit"
)

func TestTrackDownloadSleepMS(t *testing.T) {
	t.Parallel()
	for range 100 {
		ms := ratelimit.TrackDownloadSleepMS().Milliseconds()
		if ms < 2000 || ms > 6000 {
			t.Errorf("expected 2000 <= ms <= 6000, got %d", ms)
		}
	}
}
