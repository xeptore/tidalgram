package telegram

import (
	"context"
	"sync/atomic"

	"github.com/gotd/td/telegram/uploader"
	"github.com/rs/zerolog"
)

type Progress struct {
	logger   zerolog.Logger
	total    int64
	uploaded atomic.Int64
}

func (receiver *Progress) Chunk(ctx context.Context, state uploader.ProgressState) error {
	receiver.logger.
		Debug().
		Str("state", state.Name).
		Int64("total", receiver.total).
		Int64("uploaded", receiver.uploaded.Load()).
		Int64("part_size", int64(state.PartSize)).
		Msg("Received chunk")

	receiver.uploaded.Add(int64(state.PartSize))
	return nil
}
