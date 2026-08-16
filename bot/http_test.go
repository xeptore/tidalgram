package bot

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHTTPClientUsesDefaultTransportSafeguards(t *testing.T) {
	t.Parallel()

	proxy := func(*http.Request) (*url.URL, error) { return nil, nil }
	client := newHTTPClient(proxy)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotNil(t, transport.DialContext)
	assert.Positive(t, transport.IdleConnTimeout)
	assert.Positive(t, transport.TLSHandshakeTimeout)
	assert.True(t, transport.ForceAttemptHTTP2)
}

func TestLongPollRequestAllowsNetworkMargin(t *testing.T) {
	t.Parallel()

	serverTimeout := time.Duration(longPollTimeout) * time.Second
	assert.Greater(t, longPollRequestTimeout, serverTimeout)
}
