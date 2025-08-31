package progress

import (
	"context"
	"math"
	"sync/atomic"

	"github.com/gotd/td/telegram/uploader"
)

type AlbumTracker struct {
	total int64
	files []*FileTracker
}

func NewAlbumTracker(size int) *AlbumTracker {
	return &AlbumTracker{
		total: 0,
		files: make([]*FileTracker, 0, size),
	}
}

func (p *AlbumTracker) Set(i int, f *FileTracker) {
	p.total += f.total
	p.files[i] = f
}

func (p *AlbumTracker) At(i int) (*Cover, *Track) {
	f := p.files[i]
	return f.cover, f.track
}

func (p *AlbumTracker) Percent(ctx context.Context) int {
	var uploaded int64
	for _, f := range p.files {
		uploaded += f.cover.uploaded.Load() + f.track.uploaded.Load()
	}

	return int(math.Floor(float64(uploaded) / float64(p.total) * 100))
}

type AlbumFile struct {
	atomic.Int64
}

func (f *AlbumFile) Chunk(ctx context.Context, state uploader.ProgressState) error {
	f.Store(state.Uploaded)
	return nil
}

type FileTracker struct {
	total int64
	cover *Cover
	track *Track
}

func NewFileTracker(cover *Cover, track *Track) *FileTracker {
	return &FileTracker{
		total: cover.Total + track.Total,
		cover: cover,
		track: track,
	}
}

func (f *FileTracker) Percent() int {
	uploaded := f.cover.uploaded.Load() + f.track.uploaded.Load()
	return int(math.Floor(float64(uploaded) / float64(f.total) * 100))
}

type Cover struct {
	Total    int64
	uploaded atomic.Int64
}

func (f *Cover) Chunk(ctx context.Context, state uploader.ProgressState) error {
	f.uploaded.Store(state.Uploaded)
	return nil
}

type Track struct {
	Total    int64
	uploaded atomic.Int64
}

func (f *Track) Chunk(ctx context.Context, state uploader.ProgressState) error {
	f.uploaded.Store(state.Uploaded)
	return nil
}
