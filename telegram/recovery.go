package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/rs/zerolog"
)

type recoveryMiddleware struct {
	stop       <-chan struct{}
	logger     zerolog.Logger
	newBackoff func() backoff.BackOff
}

func newRecoveryMiddleware(
	ctx context.Context,
	logger zerolog.Logger,
	timeout time.Duration,
) telegram.Middleware {
	return &recoveryMiddleware{
		stop:   ctx.Done(),
		logger: logger,
		newBackoff: func() backoff.BackOff {
			return backoff.NewExponentialBackOff(
				backoff.WithMultiplier(1.1),
				backoff.WithMaxElapsedTime(timeout),
				backoff.WithMaxInterval(10*time.Second),
			)
		},
	}
}

func (r *recoveryMiddleware) Handle(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		b := backoff.WithContext(r.newBackoff(), ctx)

		err := backoff.RetryNotify(
			func() error {
				if err := next.Invoke(ctx, input, output); nil != err {
					if r.shouldRecover(ctx, err) {
						return fmt.Errorf("recoverable telegram request: %w", err)
					}

					return backoff.Permanent(fmt.Errorf("telegram request: %w", err))
				}

				return nil
			},
			b,
			func(err error, duration time.Duration) {
				r.logger.
					Debug().
					Err(err).
					Dur("duration", duration).
					Msg("Waiting for Telegram connection recovery")
			},
		)
		if nil != err {
			return fmt.Errorf("recover telegram connection: %w", err)
		}

		return nil
	}
}

func (r *recoveryMiddleware) shouldRecover(ctx context.Context, err error) bool {
	select {
	case <-r.stop:
		return false
	case <-ctx.Done():
		return false
	default:
	}

	_, isTelegramErr := tgerr.As(err)

	return !isTelegramErr
}
