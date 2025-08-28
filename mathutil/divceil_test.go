package mathutil_test

import (
	"fmt"
	"testing"

	"github.com/xeptore/tidalgram/mathutil"
)

func TestDivCeil(t *testing.T) {
	t.Parallel()

	tests := []struct {
		a, b, expected int
	}{
		{1, 1, 1},
		{1, 2, 1},
		{2, 1, 2},
		{2, 2, 1},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("a=%d,b=%d", test.a, test.b), func(t *testing.T) {
			t.Parallel()

			actual := mathutil.DivCeil(test.a, test.b)
			if actual != test.expected {
				t.Errorf("expected %d, got %d", test.expected, actual)
			}
		})
	}
}
