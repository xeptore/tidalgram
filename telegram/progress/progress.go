package progress

import (
	"context"
	"math"
	"sync/atomic"

	"github.com/gotd/td/telegram/uploader"
)

type Monitor interface {
	Percent() int
}

type BatchMonitor struct {
	total  int64
	tracks []BatchTrack
}

func NewBatchMonitor(size int) *BatchMonitor {
	return &BatchMonitor{
		total:  0,
		tracks: make([]BatchTrack, size),
	}
}

func (p *BatchMonitor) Set(i int, t *Track, c *Cover) {
	p.total += t.Size + c.Size
	p.tracks[i] = BatchTrack{
		cover: c,
		track: t,
	}
}

func (p *BatchMonitor) At(i int) (*Track, *Cover) {
	f := p.tracks[i]
	return f.track, f.cover
}

func (p *BatchMonitor) Percent() int {
	var uploaded int64
	for _, f := range p.tracks {
		uploaded += f.cover.uploaded.Load() + f.track.uploaded.Load()
	}

	return int(math.Floor(float64(uploaded) / float64(p.total) * 100))
}

type BatchTrack struct {
	cover *Cover
	track *Track
}

type AlbumMonitor struct {
	total  int64
	tracks []AlbumTrack
}

func NewAlbumMonitor(size int) *AlbumMonitor {
	return &AlbumMonitor{
		total:  0,
		tracks: make([]AlbumTrack, size),
	}
}

func (p *AlbumMonitor) Set(i int, t *Track) {
	p.total += t.Size
	p.tracks[i] = AlbumTrack{
		track: t,
	}
}

func (p *AlbumMonitor) At(i int) *Track {
	return p.tracks[i].track
}

func (p *AlbumMonitor) Percent() int {
	var uploaded int64
	for _, f := range p.tracks {
		uploaded += f.track.uploaded.Load()
	}

	return int(math.Floor(float64(uploaded) / float64(p.total) * 100))
}

type AlbumTrack struct {
	track *Track
}

type TrackMonitor struct {
	total int64
	cover *Cover
	track *Track
}

func NewTrackMonitor(cover *Cover, track *Track) *TrackMonitor {
	return &TrackMonitor{
		total: cover.Size + track.Size,
		cover: cover,
		track: track,
	}
}

func (f *TrackMonitor) Percent() int {
	uploaded := f.cover.uploaded.Load() + f.track.uploaded.Load()
	return int(math.Floor(float64(uploaded) / float64(f.total) * 100))
}

type Cover struct {
	Size     int64
	uploaded atomic.Int64
}

func (c *Cover) Chunk(ctx context.Context, state uploader.ProgressState) error {
	c.uploaded.Store(state.Uploaded)
	return nil
}

type Track struct {
	Size     int64
	uploaded atomic.Int64
}

func (t *Track) Chunk(ctx context.Context, state uploader.ProgressState) error {
	t.uploaded.Store(state.Uploaded)
	return nil
}
