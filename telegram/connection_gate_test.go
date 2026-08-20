package telegram_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gotd/td/proto/codec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xeptore/tidalgram/telegram"
)

func TestConnectionGateCoalescesTransportFloods(t *testing.T) {
	t.Parallel()

	gate := telegram.NewConnectionGate()
	firstUntil, firstStarted := gate.Block()
	secondUntil, secondStarted := gate.Block()

	assert.True(t, firstStarted)
	assert.False(t, secondStarted)
	assert.Equal(t, firstUntil, secondUntil)
	assert.True(t, gate.Blocked())
}

func TestConnectionGateWaitHonorsContext(t *testing.T) {
	t.Parallel()

	gate := telegram.NewConnectionGate()
	gate.Block()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.ErrorIs(t, gate.Wait(ctx), context.Canceled)
}

func TestIsTransportFlood(t *testing.T) {
	t.Parallel()

	floodErr := fmt.Errorf("read loop: %w", codec.ProtocolErr{Code: codec.CodeTransportFlood})
	assert.True(t, telegram.IsTransportFlood(floodErr))
	assert.False(t, telegram.IsTransportFlood(context.DeadlineExceeded))
}

func TestConnectionGateSpacesDialAttempts(t *testing.T) {
	t.Parallel()

	gate := telegram.NewConnectionGate()
	require.NoError(t, gate.Wait(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), telegram.DialInterval/10)
	defer cancel()
	err := gate.Wait(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}
