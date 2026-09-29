package telegram_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/xeptore/tidalgram/telegram"
)

func TestContainsTrackID(t *testing.T) {
	t.Parallel()

	require.True(t, telegram.ContainsTrackID("🆔 123456", "123456"))
	require.True(t, telegram.ContainsTrackID("before 123456 after", "123456"))
	require.False(t, telegram.ContainsTrackID("🆔 1234567", "123456"))
	require.False(t, telegram.ContainsTrackID("🆔 0123456", "123456"))
	require.False(t, telegram.ContainsTrackID("no id here", "123456"))
	require.False(t, telegram.ContainsTrackID("123", ""))
}
