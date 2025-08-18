package result

type Of[T any] struct {
	v   *T
	err error
}

func (r Of[T]) Unwrap() *T {
	if nil != r.err {
		panic("cannot get value of error result")
	}

	return r.v
}

func (r Of[T]) Err() error {
	return r.err
}

func Ok[T any](v *T) Of[T] {
	return Of[T]{v: v, err: nil}
}

func Err[T any](err error) Of[T] {
	return Of[T]{err: err, v: nil}
}
