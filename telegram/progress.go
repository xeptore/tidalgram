package telegram

import (
	"context"
	"sync/atomic"

	"github.com/gotd/td/telegram/uploader"
)

type Progress struct {
	children []*ChildProgress
}

type ChildProgress struct {
	total    int64
	uploaded atomic.Int64
}

func (receiver *ChildProgress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	receiver.uploaded.Store(int64(state.Uploaded))
	return nil
}
