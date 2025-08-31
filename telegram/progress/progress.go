package progress

import (
	"context"
	"fmt"
	"math"
	"sync/atomic"

	"github.com/gotd/td/telegram/uploader"
)

type Tracker interface {
	Percent() int
}

type AlbumTracker struct {
	total int64
	files []*FileTracker
}

func NewAlbumTracker(size int) *AlbumTracker {
	return &AlbumTracker{
		total: 0,
		files: make([]*FileTracker, size),
	}
}

func (p *AlbumTracker) Set(i int, f *FileTracker) {
	p.total += f.total
	p.files[i] = f
}

func (p *AlbumTracker) At(i int) (*Track, *Cover) {
	f := p.files[i]
	return f.track, f.cover
}

func (p *AlbumTracker) PrintTotal() {
	fmt.Printf("Total size: %d\n", p.total)
}

func (p *AlbumTracker) Percent() int {
	var uploaded int64
	for _, f := range p.files {
		uploaded += f.cover.uploaded.Load() + f.track.uploaded.Load()
	}

	return int(math.Floor(float64(uploaded) / float64(p.total) * 100))
}

type FileTracker struct {
	total int64
	cover *Cover
	track *Track
}

func NewFileTracker(cover *Cover, track *Track) *FileTracker {
	return &FileTracker{
		total: cover.Size + track.Size,
		cover: cover,
		track: track,
	}
}

func (f *FileTracker) Percent() int {
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
