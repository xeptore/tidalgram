package bot_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xeptore/tidalgram/bot"
)

func TestIsTidalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		expected bool
	}{
		// Valid URLs with standard format
		{
			name:     "valid album URL",
			url:      "https://tidal.com/album/123",
			expected: true,
		},
		{
			name:     "valid track URL",
			url:      "https://tidal.com/track/456",
			expected: true,
		},
		{
			name:     "valid playlist URL",
			url:      "https://tidal.com/playlist/789",
			expected: true,
		},
		{
			name:     "valid artist URL",
			url:      "https://tidal.com/artist/101112",
			expected: true,
		},
		{
			name:     "valid mix URL",
			url:      "https://tidal.com/mix/131415",
			expected: true,
		},
		{
			name:     "valid video URL",
			url:      "https://tidal.com/video/161718",
			expected: true,
		},

		// Valid URLs with "browse" prefix
		{
			name:     "valid album URL with browse prefix",
			url:      "https://tidal.com/browse/album/123",
			expected: true,
		},
		{
			name:     "valid track URL with browse prefix",
			url:      "https://tidal.com/browse/track/456",
			expected: true,
		},
		{
			name:     "valid playlist URL with browse prefix",
			url:      "https://tidal.com/browse/playlist/789",
			expected: true,
		},
		{
			name:     "valid artist URL with browse prefix",
			url:      "https://tidal.com/browse/artist/101112",
			expected: true,
		},
		{
			name:     "valid mix URL with browse prefix",
			url:      "https://tidal.com/browse/mix/131415",
			expected: true,
		},
		{
			name:     "valid video URL with browse prefix",
			url:      "https://tidal.com/browse/video/161718",
			expected: true,
		},

		// Valid URLs with "/u" suffix
		{
			name:     "valid album URL with /u suffix",
			url:      "https://tidal.com/album/123/u",
			expected: true,
		},
		{
			name:     "valid track URL with /u suffix",
			url:      "https://tidal.com/track/456/u",
			expected: true,
		},
		{
			name:     "valid playlist URL with /u suffix",
			url:      "https://tidal.com/playlist/789/u",
			expected: true,
		},
		{
			name:     "valid artist URL with /u suffix",
			url:      "https://tidal.com/artist/101112/u",
			expected: true,
		},
		{
			name:     "valid mix URL with /u suffix",
			url:      "https://tidal.com/mix/131415/u",
			expected: true,
		},
		{
			name:     "valid video URL with /u suffix",
			url:      "https://tidal.com/video/161718/u",
			expected: true,
		},

		// Valid URLs with both "browse" prefix and "/u" suffix
		{
			name:     "valid album URL with browse prefix and /u suffix",
			url:      "https://tidal.com/browse/album/123/u",
			expected: true,
		},
		{
			name:     "valid track URL with browse prefix and /u suffix",
			url:      "https://tidal.com/browse/track/456/u",
			expected: true,
		},

		// Valid URLs with different hosts
		{
			name:     "valid album URL with www.tidal.com host",
			url:      "https://www.tidal.com/album/123",
			expected: true,
		},
		{
			name:     "valid album URL with listen.tidal.com host",
			url:      "https://listen.tidal.com/album/123",
			expected: true,
		},

		// Invalid URLs - missing ID
		{
			name:     "invalid album URL with browse prefix but missing ID",
			url:      "https://tidal.com/browse/album",
			expected: false,
		},
		{
			name:     "invalid track URL with /u suffix but missing ID",
			url:      "https://tidal.com/track/u",
			expected: false,
		},
		{
			name:     "invalid album URL missing ID",
			url:      "https://tidal.com/album",
			expected: false,
		},
		{
			name:     "invalid track URL missing ID",
			url:      "https://tidal.com/track",
			expected: false,
		},
		{
			name:     "invalid playlist URL missing ID",
			url:      "https://tidal.com/playlist",
			expected: false,
		},
		{
			name:     "invalid browse with only one part",
			url:      "https://tidal.com/browse",
			expected: false,
		},
		{
			name:     "invalid browse with missing ID",
			url:      "https://tidal.com/browse/mix",
			expected: false,
		},

		// Invalid URLs - wrong scheme
		{
			name:     "invalid album URL with http scheme",
			url:      "http://tidal.com/album/123",
			expected: false,
		},
		{
			name:     "invalid album URL with ftp scheme",
			url:      "ftp://tidal.com/album/123",
			expected: false,
		},

		// Invalid URLs - wrong host
		{
			name:     "invalid album URL with wrong host",
			url:      "https://spotify.com/album/123",
			expected: false,
		},
		{
			name:     "invalid album URL with subdomain",
			url:      "https://music.tidal.com/album/123",
			expected: false,
		},

		// Invalid URLs - unsupported resource type
		{
			name:     "invalid URL with unsupported resource type",
			url:      "https://tidal.com/user/123",
			expected: false,
		},
		{
			name:     "invalid URL with unsupported resource type and browse",
			url:      "https://tidal.com/browse/user/123",
			expected: false,
		},

		// Invalid URLs - malformed
		{
			name:     "invalid URL - not a URL",
			url:      "not a url",
			expected: false,
		},
		{
			name:     "invalid URL - empty string",
			url:      "",
			expected: false,
		},
		{
			name:     "invalid URL - only domain",
			url:      "https://tidal.com",
			expected: false,
		},
		{
			name:     "invalid URL - only slash",
			url:      "https://tidal.com/",
			expected: false,
		},

		// Edge cases with trailing slashes
		{
			name:     "valid album URL with trailing slash",
			url:      "https://tidal.com/album/123/",
			expected: true,
		},
		{
			name:     "valid album URL with browse and trailing slash",
			url:      "https://tidal.com/browse/album/123/",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := bot.IsTidalURL(tt.url)
			assert.Equal(t, tt.expected, result, "URL: %s", tt.url)
		})
	}
}
