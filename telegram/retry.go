package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/rs/zerolog"
)

const maxRPCRetries = 5

var retryableRPCErrors = []string{
	"Timedout",
	"No workers running",
	"RPC_CALL_FAIL",
	"RPC_MCGET_FAIL",
	"WORKER_BUSY_TOO_LONG_RETRY",
	"memory limit exit",
}

type retryMiddleware struct {
	logger     zerolog.Logger
	maxRetries int
	errors     []string
}

func newRetryMiddleware(logger zerolog.Logger, maxRetries int) telegram.Middleware {
	return &retryMiddleware{
		logger:     logger,
		maxRetries: maxRetries,
		errors:     retryableRPCErrors,
	}
}

func (r *retryMiddleware) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		var lastErr error

		for attempt := range r.maxRetries {
			err := next.Invoke(ctx, input, output)
			if nil == err {
				return nil
			}

			lastErr = err
			if !tgerr.Is(err, r.errors...) {
				return fmt.Errorf("telegram request: %w", err)
			}

			r.logger.
				Debug().
				Err(err).
				Int("attempt", attempt+1).
				Msg("Retrying Telegram request")
		}

		return fmt.Errorf("telegram request retry limit reached after %d attempts: %w", r.maxRetries, lastErr)
	}
}
