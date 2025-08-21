package must

func Be(expr bool, msg string) {
	if !expr {
		panic("assertion failed: " + msg)
	}
}

func NilErr(err error) {
	if nil != err {
		panic("expected nil error, got: " + err.Error())
	}
}
