package telegram

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gotd/td/proto/codec"
)

const (
	telegramDialInterval           = time.Second
	telegramTransportFloodCooldown = 30 * time.Second
)

// connectionGate spaces out MTProto connection attempts and temporarily stops
// them after Telegram rejects a connection with transport error 429.
type connectionGate struct {
	mu           sync.Mutex
	nextDial     time.Time
	blockedUntil time.Time
	changed      chan struct{}
}

func newConnectionGate() *connectionGate {
	return &connectionGate{changed: make(chan struct{})}
}

func (g *connectionGate) Wait(ctx context.Context) error {
	for {
		g.mu.Lock()
		now := time.Now()
		readyAt := g.nextDial
		if g.blockedUntil.After(readyAt) {
			readyAt = g.blockedUntil
		}
		if !readyAt.After(now) {
			g.nextDial = now.Add(telegramDialInterval)
			g.mu.Unlock()

			return nil
		}
		changed := g.changed
		g.mu.Unlock()

		timer := time.NewTimer(time.Until(readyAt))
		select {
		case <-ctx.Done():
			stopAndDrainTimer(timer)

			return ctx.Err()
		case <-changed:
			stopAndDrainTimer(timer)
		case <-timer.C:
		}
	}
}

func stopAndDrainTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (g *connectionGate) Block() (until time.Time, started bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	if g.blockedUntil.After(now) {
		return g.blockedUntil, false
	}

	g.blockedUntil = now.Add(telegramTransportFloodCooldown)
	close(g.changed)
	g.changed = make(chan struct{})

	return g.blockedUntil, true
}

func (g *connectionGate) Blocked() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.blockedUntil.After(time.Now())
}

func isTransportFlood(err error) bool {
	var protocolErr *codec.ProtocolErr

	return errors.As(err, &protocolErr) && protocolErr.Code == codec.CodeTransportFlood
}
