package telegram

import (
	"fmt"
	"slices"
	"sync"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/rs/zerolog"
)

type dcPool struct {
	client      *telegram.Client
	size        int64
	logger      zerolog.Logger
	mu          sync.Mutex
	middlewares []telegram.Middleware
	invoker     tg.Invoker
	closePool   func() error
}

func newDCPool(
	client *telegram.Client,
	size int64,
	logger zerolog.Logger,
	middlewares ...telegram.Middleware,
) *dcPool {
	return &dcPool{
		client:      client,
		size:        size,
		logger:      logger,
		mu:          sync.Mutex{},
		middlewares: middlewares,
		invoker:     nil,
		closePool:   nil,
	}
}

func (p *dcPool) Default() *tg.Client {
	p.mu.Lock()
	defer p.mu.Unlock()

	if nil != p.invoker {
		return tg.NewClient(p.invoker)
	}

	invoker, err := p.client.Pool(p.size)
	if nil != err {
		p.logger.Error().Err(err).Msg("Failed to create DC connection pool")
		p.invoker = p.client

		return p.client.API()
	}

	p.closePool = invoker.Close
	p.invoker = chainMiddlewares(invoker, p.middlewares...)

	return tg.NewClient(p.invoker)
}

func (p *dcPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if nil == p.closePool {
		return nil
	}

	if err := p.closePool(); nil != err {
		return fmt.Errorf("close DC pool: %v", err)
	}

	return nil
}

func chainMiddlewares(invoker tg.Invoker, chain ...telegram.Middleware) tg.Invoker {
	if len(chain) == 0 {
		return invoker
	}

	for _, c := range slices.Backward(chain) {
		invoker = c.Handle(invoker)
	}

	return invoker
}
