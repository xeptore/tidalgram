package mathutil_test

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"

	"github.com/xeptore/tidalgram/mathutil"
)

// --- helpers used in a couple tests ---

func sizeOf[T any]() uintptr {
	var zero T
	return unsafe.Sizeof(zero)
}

func firstPtr[T any](s []T) uintptr {
	if len(s) == 0 {
		return 0
	}

	return uintptr(unsafe.Pointer(&s[0]))
}

// --- tests ---

func TestMakeAlbumShape_NilInput(t *testing.T) {
	t.Parallel()

	var src [][]int
	dst := mathutil.MakeAlbumShape[int, int](src)
	assert.Nil(t, dst)
}

func TestMakeAlbumShape_EmptyOuter(t *testing.T) {
	t.Parallel()

	src := [][]int{}
	dst := mathutil.MakeAlbumShape[int, int](src)
	assert.NotNil(t, dst)
	assert.Empty(t, dst)
}

func TestMakeAlbumShape_PreservesShape(t *testing.T) {
	t.Parallel()

	src := [][]int{
		{1, 2, 3},
		{4},
		{},
		{5, 6},
	}
	dst := mathutil.MakeAlbumShape[int, string](src)

	assert.Len(t, dst, len(src))

	wantLens := []int{3, 1, 0, 2}
	for i := range src {
		assert.Len(t, dst[i], wantLens[i], "row %d length", i)
	}
}

func TestMakeAlbumShape_ZeroValues_Int(t *testing.T) {
	t.Parallel()

	src := [][]string{{"a"}, {"b", "c"}}
	dst := mathutil.MakeAlbumShape[string, int](src)

	for i := range dst {
		for j := range dst[i] {
			assert.Equal(t, 0, dst[i][j], "dst[%d][%d]", i, j)
		}
	}
}

func TestMakeAlbumShape_ZeroValues_String(t *testing.T) {
	t.Parallel()

	src := [][]int{{1, 2}, {}, {3}}
	dst := mathutil.MakeAlbumShape[int, string](src)

	for i := range dst {
		for j := range dst[i] {
			assert.Empty(t, dst[i][j], "dst[%d][%d]", i, j)
		}
	}
}

func TestMakeAlbumShape_CapEqualsLen(t *testing.T) {
	t.Parallel()

	src := [][]byte{{1}, {2, 3}, {}, {4, 5, 6}}
	dst := mathutil.MakeAlbumShape[byte, byte](src)

	for i := range dst {
		assert.Len(t, dst[i], cap(dst[i]), "row %d", i)
	}
}

func TestMakeAlbumShape_RowsAreDisjoint(t *testing.T) {
	t.Parallel()

	src := [][]int{{0, 0}, {0}, {0, 0, 0}}
	dst := mathutil.MakeAlbumShape[int, int](src)

	dst[0][1] = 42
	assert.Empty(t, dst[1][0], "row separation broken")

	dst[2][2] = 7
	assert.NotEmpty(t, dst[0][1], "row 0 changed after writing row 2")
}

func TestMakeAlbumShape_ContiguousUnsafeCheck(t *testing.T) {
	t.Parallel()

	type T struct{ A, B int }
	src := [][]byte{
		{1, 2, 3},
		{4},
		{},
		{5, 6},
	}
	dst := mathutil.MakeAlbumShape[byte, T](src) // shape [3,1,0,2] of T

	sz := sizeOf[T]()
	var prevAddr uintptr
	var prevLen int
	hadPrev := false

	for i := range dst {
		if len(dst[i]) == 0 {
			continue
		}
		addr := firstPtr(dst[i])
		if hadPrev {
			want := prevAddr + uintptr(prevLen)*sz
			assert.Equal(t, want, addr, "row %d not contiguous with previous", i)
		}
		prevAddr = addr
		prevLen = len(dst[i])
		hadPrev = true
	}
}

func TestMakeAlbumShape_AppendReallocatesPerRow(t *testing.T) {
	t.Parallel()

	src := [][]int{{0, 0}, {0, 0, 0}}
	dst := mathutil.MakeAlbumShape[int, int](src)

	before := firstPtr(dst[0])
	dst[0] = append(dst[0], 99) // cap==len; should realloc
	after := firstPtr(dst[0])

	assert.NotEqual(t, before, after, "row 0 should reallocate on append")

	// Row 1 should be unaffected
	row1ptr := firstPtr(dst[1])
	assert.NotZero(t, row1ptr, "row 1 unexpectedly empty")
	for _, v := range dst[1] {
		assert.Exactly(t, 0, v)
	}
}

func TestMakeAlbumShape_DifferentTypes_WithEmptyRows(t *testing.T) {
	t.Parallel()

	type K = complex64
	type T = *struct{ X int }

	src := [][]K{
		{1 + 2i, 3 + 4i},
		{},
		{5 + 6i},
	}
	dst := mathutil.MakeAlbumShape[K, T](src)

	assert.Len(t, dst, 3)
	assert.Len(t, dst[0], 2)
	assert.Empty(t, dst[1])
	assert.Len(t, dst[2], 1)

	// zero value for *struct{...} is nil
	assert.Nil(t, dst[0][0])
	assert.Nil(t, dst[0][1])
	if len(dst[2]) == 1 {
		assert.Nil(t, dst[2][0])
	}
}

func TestMakeAlbumShape_NoAliasingWithSource(t *testing.T) {
	t.Parallel()

	src := [][]int{
		{1, 2},
		{3},
	}
	dst := mathutil.MakeAlbumShape[int, int](src)

	// Mutate src; dst should be unaffected (we didn't copy data, only shape)
	src[0][0] = 99
	assert.Empty(t, dst[0][0], "dst should hold zero values independent of src data")
}
