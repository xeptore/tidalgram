package telegram

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/telegram"
	"github.com/rs/zerolog"
)

func connect(
	ctx context.Context,
	logger zerolog.Logger,
	c *telegram.Client,
	waiter *floodwait.Waiter,
) (func() error, error) {
	ctx, cancel := context.WithCancel(ctx)

	var (
		wg       sync.WaitGroup
		runErr   = make(chan error, 1)
		initDone = make(chan struct{})
	)
	defer wg.Wait()

	wg.Go(func() {
		defer func() {
			logger.Debug().Msg("Closing runErr channel")
			close(runErr)
		}()

		runErr <- waiter.Run(ctx, func(ctx context.Context) error {
			return c.Run(ctx, func(ctx context.Context) error {
				logger.Debug().Msg("Closing initDone channel")
				close(initDone)

				<-ctx.Done()
				if errors.Is(ctx.Err(), context.Canceled) {
					return nil
				}

				return ctx.Err()
			})
		})
	})

	select {
	case <-ctx.Done():
		cancel()

		if err := ctx.Err(); nil != err {
			return func() error { return nil }, fmt.Errorf("context done: %w", err)
		}

		return func() error { return nil }, nil
	case err := <-runErr:
		cancel()
		return func() error { return nil }, err
	case <-initDone:
	}

	stopFunc := func() error {
		cancel()
		return <-runErr
	}

	return stopFunc, nil
}
