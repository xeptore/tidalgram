package telegram

import (
	"context"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/contrib/middleware/ratelimit"
	"github.com/gotd/td/telegram"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

func newPoolMiddlewares(
	ctx context.Context,
	logger zerolog.Logger,
	timeout time.Duration,
) []telegram.Middleware {
	return []telegram.Middleware{
		newRecoveryMiddleware(ctx, logger, timeout),
		newRetryMiddleware(logger, maxRPCRetries),
		floodwait.NewSimpleWaiter(),
	}
}

func newWaiterMiddleware(logger zerolog.Logger) *floodwait.Waiter {
	return floodwait.
		NewWaiter().
		WithCallback(func(ctx context.Context, wait floodwait.FloodWait) {
			logger.
				Warn().
				Dur("duration", wait.Duration).
				Msg("Got FLOOD_WAIT. Will retry after")
		})
}

func newSimpleWaiterMiddleware() *floodwait.SimpleWaiter {
	return floodwait.
		NewSimpleWaiter().
		WithMaxRetries(100).
		WithMaxWait(time.Second * 20)
}

func newRateLimitMiddleware() *ratelimit.RateLimiter {
	return ratelimit.New(rate.Every(time.Millisecond*100), 5)
}
