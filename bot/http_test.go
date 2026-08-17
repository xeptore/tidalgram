package bot_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xeptore/tidalgram/bot"
)

func TestNewHTTPClientUsesDefaultTransportSafeguards(t *testing.T) {
	t.Parallel()

	proxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	client := bot.NewHTTPClient(proxy)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotNil(t, transport.DialContext)
	assert.Positive(t, transport.IdleConnTimeout)
	assert.Positive(t, transport.TLSHandshakeTimeout)
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestLongPollRequestAllowsNetworkMargin(t *testing.T) {
	t.Parallel()

	serverTimeout := time.Duration(bot.LongPollTimeout) * time.Second
	assert.Greater(t, bot.LongPollRequestTimeout, serverTimeout)
}
