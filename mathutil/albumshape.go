package mathutil

func MakeAlbumShape[T, K any](in [][]T) [][]K {
	if in == nil {
		return nil
	}

	total := 0
	for _, row := range in {
		l := len(row)
		if l < 0 {
			panic("negative length")
		}
		nt := total + l
		if nt < total { // overflow wrap
			panic("length overflow")
		}
		total = nt
	}

	var (
		out  = make([][]K, len(in))
		data = make([]K, total)
		off  int
	)
	for i, row := range in {
		l := len(row)
		out[i] = data[off : off+l : off+l]
		off += l
	}

	return out
}
