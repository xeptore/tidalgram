package telegram

import (
	"errors"
	"time"

	"github.com/karlseguin/ccache/v3"

	"github.com/xeptore/tidalgram/tidal/types"
)

const (
	// Track metadata is an optimization around the info file written at download time.
	DefaultTrackMetaTTL = time.Hour

	trackMetaCacheSize  = 10_000
	cacheGetsPerPromote = 3
	cachePercentToPrune = 10
)

type memoryTrackMetaCache struct {
	c *ccache.Cache[types.StoredTrack]
}

func newTrackMetaCache() *memoryTrackMetaCache {
	return &memoryTrackMetaCache{
		c: ccache.New(
			ccache.Configure[types.StoredTrack]().
				MaxSize(trackMetaCacheSize).
				GetsPerPromote(cacheGetsPerPromote).
				PercentToPrune(cachePercentToPrune),
		),
	}
}

func (c *memoryTrackMetaCache) Get(trackID string) (types.StoredTrack, bool) {
	item := c.c.Get(TrackMetaCacheKey(trackID))
	if nil == item || item.Expired() {
		return types.StoredTrack{}, false //nolint:exhaustruct_v5
	}

	return item.Value(), true
}

func (c *memoryTrackMetaCache) Put(trackID string, meta types.StoredTrack) error {
	if len(trackID) == 0 {
		return errors.New("track metadata cache key is empty")
	}

	c.c.Set(TrackMetaCacheKey(trackID), meta, DefaultTrackMetaTTL)

	return nil
}
