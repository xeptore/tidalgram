package telegram_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/xeptore/tidalgram/telegram"
)

func TestUploadedDocumentStoreReplacesPreviousTrackDigests(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "telegram.db")
	storage, err := telegram.NewStorage(path)
	require.NoError(t, err)
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		require.NoError(t, storage.Close())
	})

	first := sampleDocument(11, []byte("first-ref"))
	second := sampleDocument(22, []byte("second-ref"))
	second.MessageID = 90
	otherTrack := sampleDocument(33, []byte("other-ref"))
	otherTrack.MessageID = 91

	require.NoError(t, storage.PutUploadedDocument("123", "digest-a", first))
	require.NoError(t, storage.PutUploadedDocument("999", "digest-a", otherTrack))
	require.NoError(t, storage.PutUploadedDocument("123", "digest-b", second))
	require.NoError(t, storage.Close())
	closed = true

	reopened := openStorage(t, path)

	_, ok, err := reopened.GetUploadedDocument("123", "digest-a")
	require.NoError(t, err)
	require.False(t, ok)

	got, ok, err := reopened.GetUploadedDocument("123", "digest-b")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, second.ID, got.ID)
	require.Equal(t, second.AccessHash, got.AccessHash)
	require.Equal(t, second.FileReference, got.FileReference)
	require.Equal(t, second.MessageID, got.MessageID)
	require.Equal(t, second.Peer, got.Peer)

	kept, ok, err := reopened.GetUploadedDocument("999", "digest-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, otherTrack.ID, kept.ID)
	require.Equal(t, []byte("other-ref"), kept.FileReference)
}

func TestUploadedDocumentStoreUpdatesSameDigest(t *testing.T) {
	t.Parallel()

	storage := openStorage(t, filepath.Join(t.TempDir(), "telegram.db"))
	original := sampleDocument(11, []byte("stale-ref"))
	refreshed := original
	refreshed.FileReference = []byte("fresh-ref")
	refreshed.AccessHash = 77

	require.NoError(t, storage.PutUploadedDocument("123", "digest-a", original))
	require.NoError(t, storage.PutUploadedDocument("123", "digest-a", refreshed))

	got, ok, err := storage.GetUploadedDocument("123", "digest-a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte("fresh-ref"), got.FileReference)
	require.Equal(t, int64(77), got.AccessHash)
	require.Equal(t, original.ID, got.ID)
	require.Equal(t, original.MessageID, got.MessageID)
}

func TestUploadedDocumentStoreRejectsEmptyFileReference(t *testing.T) {
	t.Parallel()

	storage := openStorage(t, filepath.Join(t.TempDir(), "telegram.db"))
	doc := sampleDocument(11, nil)

	err := storage.PutUploadedDocument("123", "digest-a", doc)
	require.ErrorContains(t, err, "file reference is empty")

	_, ok, err := storage.GetUploadedDocument("123", "digest-a")
	require.NoError(t, err)
	require.False(t, ok)
}

func openStorage(t *testing.T, path string) *telegram.Storage {
	t.Helper()

	storage, err := telegram.NewStorage(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, storage.Close())
	})

	return storage
}
