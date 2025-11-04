package cache

import (
	"fmt"
	"sync"
	"time"

	"github.com/karlseguin/ccache/v3"

	"github.com/xeptore/tidalgram/tidal/types"
)

var (
	DefaultDownloadedCoverTTL = 1 * time.Hour
	DefaultAlbumTTL           = 1 * time.Hour
	DefaultUploadedCoverTTL   = 1 * time.Hour
	DefaultTrackCreditsTTL    = 1 * time.Hour
)

type Cache struct {
	AlbumsMeta   AlbumsMetaCache
	Covers       DownloadedCoversCache
	TrackCredits TrackCreditsCache
}

func New() *Cache {
	albumsMetaCache := ccache.New(
		ccache.Configure[*types.AlbumMeta]().
			MaxSize(1000).
			GetsPerPromote(3).
			PercentToPrune(10),
	)

	downloadedCoversCache := ccache.New(
		ccache.Configure[[]byte]().
			MaxSize(100).
			GetsPerPromote(3).
			PercentToPrune(10),
	)

	trackCreditsCache := ccache.New(
		ccache.Configure[*types.TrackCredits]().
			MaxSize(10_000).
			GetsPerPromote(3).
			PercentToPrune(10),
	)

	return &Cache{
		AlbumsMeta: AlbumsMetaCache{
			c:   albumsMetaCache,
			mux: sync.Mutex{},
		},
		Covers: DownloadedCoversCache{
			c:   downloadedCoversCache,
			mux: sync.Mutex{},
		},
		TrackCredits: TrackCreditsCache{
			c:   trackCreditsCache,
			mux: sync.Mutex{},
		},
	}
}

type DownloadedCoversCache struct {
	c   *ccache.Cache[[]byte]
	mux sync.Mutex
}

func (dcc *DownloadedCoversCache) Fetch(
	k string,
	ttl time.Duration,
	fetch func() ([]byte, error),
) (*ccache.Item[[]byte], error) {
	dcc.mux.Lock()
	defer dcc.mux.Unlock()

	v, err := dcc.c.Fetch(k, ttl, fetch)
	if nil != err {
		return nil, fmt.Errorf("fetch cover: %w", err)
	}

	return v, nil
}

type AlbumsMetaCache struct {
	c   *ccache.Cache[*types.AlbumMeta]
	mux sync.Mutex
}

func (amc *AlbumsMetaCache) Fetch(
	k string,
	ttl time.Duration,
	fetch func() (*types.AlbumMeta, error),
) (*ccache.Item[*types.AlbumMeta], error) {
	amc.mux.Lock()
	defer amc.mux.Unlock()

	v, err := amc.c.Fetch(k, ttl, fetch)
	if nil != err {
		return nil, fmt.Errorf("fetch album meta: %w", err)
	}

	return v, nil
}

type TrackCreditsCache struct {
	c   *ccache.Cache[*types.TrackCredits]
	mux sync.Mutex
}

func (tcc *TrackCreditsCache) Fetch(
	k string,
	ttl time.Duration,
	fetch func() (*types.TrackCredits, error),
) (*ccache.Item[*types.TrackCredits], error) {
	tcc.mux.Lock()
	defer tcc.mux.Unlock()

	v, err := tcc.c.Fetch(k, ttl, fetch)
	if nil != err {
		return nil, fmt.Errorf("fetch track credits: %w", err)
	}

	return v, nil
}

func (tcc *TrackCreditsCache) Set(k string, v *types.TrackCredits, ttl time.Duration) {
	tcc.c.Set(k, v, ttl)
}
