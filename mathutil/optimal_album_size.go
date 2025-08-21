package mathutil

func OptimalAlbumSize(total int) int {
	const maxsize = 10
	numAlbums := total / maxsize // 10%1
	if total%maxsize != 0 {
		numAlbums++
	}
	if total%numAlbums == 0 {
		return total / numAlbums
	}

	return total/numAlbums + 1
}
