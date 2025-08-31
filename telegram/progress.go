package telegram

import (
	"context"
	"sync/atomic"

	"github.com/gotd/td/telegram/uploader"
)

type Progress struct {
	total    int64
	uploaded atomic.Int64
}

func (receiver *Progress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	receiver.uploaded.Add(state.Uploaded)
	return nil
}
