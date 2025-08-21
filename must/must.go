package must

func Be(expr bool, msg string) {
	if !expr {
		panic("assertion failed: " + msg)
	}
}
