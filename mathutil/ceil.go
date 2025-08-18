package mathutil

import (
	"golang.org/x/exp/constraints"
)

func DivCeil[T constraints.Signed](a, b T) T {
	if b == 0 {
		panic("division by zero")
	}
	q, r := a/b, a%b
	sameSign := (a >= 0 && b > 0) || (a <= 0 && b < 0)
	if r != 0 && sameSign {
		q++
	}
	return q
}
