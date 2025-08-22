package iterutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/xeptore/tidalgram/iterutil"
)

func TestTimes(t *testing.T) {
	t.Parallel()

	type Res struct {
		I int
		V int
	}
	got := iterutil.Map([]int{0, 1, 2}, func(i int, v int) Res { return Res{I: i * 2, V: v + 10} })
	want := []Res{
		{I: 0, V: 10},
		{I: 2, V: 11},
		{I: 4, V: 12},
	}
	assert.Exactly(t, want, got)
}
