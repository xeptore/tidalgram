package mathutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xeptore/tidalgram/mathutil"
)

func TestOptimalAlbumSize(t *testing.T) {
	t.Parallel()

	assert.Exactly(t, 10, mathutil.OptimalAlbumSize(10))
	assert.Exactly(t, 6, mathutil.OptimalAlbumSize(11))
	assert.Exactly(t, 6, mathutil.OptimalAlbumSize(12))
	assert.Exactly(t, 7, mathutil.OptimalAlbumSize(13))
	assert.Exactly(t, 7, mathutil.OptimalAlbumSize(14))
	assert.Exactly(t, 8, mathutil.OptimalAlbumSize(15))
	assert.Exactly(t, 8, mathutil.OptimalAlbumSize(16))
	assert.Exactly(t, 9, mathutil.OptimalAlbumSize(17))
	assert.Exactly(t, 9, mathutil.OptimalAlbumSize(18))
	assert.Exactly(t, 10, mathutil.OptimalAlbumSize(19))
	assert.Exactly(t, 10, mathutil.OptimalAlbumSize(20))

	assert.Exactly(t, 7, mathutil.OptimalAlbumSize(21))
	assert.Exactly(t, 8, mathutil.OptimalAlbumSize(22))
	assert.Exactly(t, 8, mathutil.OptimalAlbumSize(23))
	assert.Exactly(t, 8, mathutil.OptimalAlbumSize(24))
	assert.Exactly(t, 9, mathutil.OptimalAlbumSize(25))
	assert.Exactly(t, 9, mathutil.OptimalAlbumSize(26))
	assert.Exactly(t, 9, mathutil.OptimalAlbumSize(27))
	assert.Exactly(t, 10, mathutil.OptimalAlbumSize(28))
	assert.Exactly(t, 10, mathutil.OptimalAlbumSize(30))
	assert.Exactly(t, 10, mathutil.OptimalAlbumSize(29))
}
