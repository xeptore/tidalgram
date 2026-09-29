package telegram_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/xeptore/tidalgram/telegram"
	"github.com/xeptore/tidalgram/tidal/types"
)

func TestTelegramAudioCacheKey(t *testing.T) {
	t.Parallel()

	same := sha256Hex([]byte("same-audio"))
	other := sha256Hex([]byte("other-audio"))

	left := telegram.TelegramAudioCacheKey("123456", same)
	again := telegram.TelegramAudioCacheKey("123456", same)
	differentBytes := telegram.TelegramAudioCacheKey("123456", other)
	differentTrack := telegram.TelegramAudioCacheKey("654321", same)

	require.Equal(t, "telegram-audio:123456:"+same, left)
	require.Equal(t, left, again)
	require.NotEqual(t, left, differentBytes)
	require.NotEqual(t, left, differentTrack)
	require.Equal(t, "track:123456:metadata", telegram.TrackMetaCacheKey("123456"))
}

func TestFileSHA256HashesFileBytes(t *testing.T) {
	t.Parallel()

	body := []byte("exact-upload-bytes")
	path := writeAudio(t, body)

	got, err := telegram.FileSHA256(t.Context(), zerolog.Nop(), path)
	require.NoError(t, err)
	require.Equal(t, sha256Hex(body), got)
}

func TestIsFileReferenceError(t *testing.T) {
	t.Parallel()

	require.True(t, telegram.IsFileReferenceError(tgerr.New(400, "FILE_REFERENCE_EXPIRED")))
	require.True(t, telegram.IsFileReferenceError(tgerr.New(400, "FILE_REFERENCE_EMPTY")))
	require.True(t, telegram.IsFileReferenceError(tgerr.New(400, "FILE_REFERENCE_INVALID")))
	require.True(t, telegram.IsFileReferenceError(fmt.Errorf("send: %w", tgerr.New(400, "FILE_REFERENCE_0_EXPIRED"))))
	require.True(t, telegram.IsFileReferenceError(tgerr.New(400, "FILE_REFERENCE_1_INVALID")))
	require.False(t, telegram.IsFileReferenceError(tgerr.New(420, "FLOOD_WAIT_3")))
	require.False(t, telegram.IsFileReferenceError(context.DeadlineExceeded))
	require.False(t, telegram.IsFileReferenceError(errors.New("network down")))
}

func TestIsMessageNotAccessible(t *testing.T) {
	t.Parallel()

	inaccessible := []error{
		telegram.ErrMessageNotAccessible,
		fmt.Errorf("deleted: %w", telegram.ErrMessageNotAccessible),
		tgerr.New(400, "CHANNEL_INVALID"),
		tgerr.New(406, "CHANNEL_PRIVATE"),
		tgerr.New(400, "CHAT_ID_INVALID"),
		tgerr.New(400, "PEER_ID_INVALID"),
		tgerr.New(400, "MESSAGE_ID_INVALID"),
		tgerr.New(400, "MSG_ID_INVALID"),
		fmt.Errorf("get channel message: %w", tgerr.New(406, "CHANNEL_PRIVATE")),
	}
	for _, err := range inaccessible {
		require.True(t, telegram.IsMessageNotAccessible(err), err)
	}

	accessible := []error{
		nil,
		context.Canceled,
		context.DeadlineExceeded,
		fmt.Errorf("get message: %w", context.DeadlineExceeded),
		tgerr.New(420, "FLOOD_WAIT_5"),
		tgerr.New(400, "USER_BANNED_IN_CHANNEL"),
		tgerr.New(403, "CHAT_WRITE_FORBIDDEN"),
		tgerr.New(500, "INTERNAL"),
		tgerr.New(500, "Timedout"),
		errors.New("temporary failure"),
		tgerr.New(400, "FILE_REFERENCE_EXPIRED"),
	}
	for _, err := range accessible {
		require.False(t, telegram.IsMessageNotAccessible(err))
	}
}

func TestSendTrackMetadataCacheHitSkipsProvider(t *testing.T) {
	t.Parallel()

	meta := testMeta("Cached")
	metaCache := newMemMetaCache()
	metaCache.items["123"] = meta
	tracks := newMemTracks(t, map[string][]byte{"123": []byte("audio")})
	tracks.meta["123"] = meta
	sender := newMemSender()
	docs := newMemDocCache()
	svc := telegram.NewTrackService(zerolog.Nop(), metaCache, docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Zero(t, tracks.metaCalls)
	require.Equal(t, 1, tracks.audioCalls)
	require.Equal(t, 1, sender.uploads)
	require.Zero(t, metaCache.puts)
}

func TestSendTrackMetadataCacheMissStoresMetadata(t *testing.T) {
	t.Parallel()

	meta := testMeta("Fetched")
	metaCache := newMemMetaCache()
	tracks := newMemTracks(t, map[string][]byte{"123": []byte("audio")})
	tracks.meta["123"] = meta
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.Nop(), metaCache, newMemDocCache(), tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Equal(t, 1, tracks.metaCalls)
	require.Equal(t, meta, metaCache.items["123"])
	require.Equal(t, 1, sender.uploads)
}

func TestSendTrackMetadataCacheWriteFailureContinues(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	meta := testMeta("Fetched")
	metaCache := newMemMetaCache()
	metaCache.putErr = errCacheWrite
	tracks := newMemTracks(t, map[string][]byte{"123": []byte("audio")})
	tracks.meta["123"] = meta
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.New(&logs), metaCache, newMemDocCache(), tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Equal(t, 1, tracks.metaCalls)
	require.Equal(t, 1, sender.uploads)
	require.Contains(t, logs.String(), "failed to cache track metadata")
	require.Contains(t, logs.String(), "123")
}

func TestSendTrackSameBytesReuseUploadedDocument(t *testing.T) {
	t.Parallel()

	body := []byte("same-bytes")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)
	peer := testPeer()

	require.NoError(t, svc.SendTrack(t.Context(), peer, "123"))
	require.NoError(t, svc.SendTrack(t.Context(), peer, "123"))

	key := telegram.TelegramAudioCacheKey("123", sha256Hex(body))
	require.Equal(t, []string{key, key}, docs.gets)
	require.Equal(t, 1, sender.uploads)
	require.Equal(t, 1, sender.sends)
	require.Equal(t, []int64{sender.uploaded[0].ID}, sender.sentIDs)
	require.Equal(t, sender.uploaded[0], docs.items[key])
}

func TestSendTrackDifferentBytesDoNotReuseOldDocument(t *testing.T) {
	t.Parallel()

	first := []byte("version-a")
	second := []byte("version-b")
	tracks := newMemTracks(t, map[string][]byte{"123": first})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)
	peer := testPeer()

	require.NoError(t, svc.SendTrack(t.Context(), peer, "123"))
	tracks.audio["123"] = telegram.TrackAudio{Path: writeAudio(t, second), CoverPath: testCoverPath}
	require.NoError(t, svc.SendTrack(t.Context(), peer, "123"))

	oldKey := telegram.TelegramAudioCacheKey("123", sha256Hex(first))
	newKey := telegram.TelegramAudioCacheKey("123", sha256Hex(second))
	require.NotEqual(t, oldKey, newKey)
	require.Contains(t, docs.gets, oldKey)
	require.Contains(t, docs.gets, newKey)
	require.Equal(t, 2, sender.uploads)
	require.Zero(t, sender.sends)
	require.NotContains(t, docs.items, oldKey)
	require.Equal(t, sender.uploaded[1], docs.items[newKey])
}

func TestSendTrackDifferentTrackIDsStayDistinct(t *testing.T) {
	t.Parallel()

	body := []byte("identical-bytes")
	tracks := newMemTracks(t, map[string][]byte{
		"111": body,
		"222": body,
	})
	tracks.meta["111"] = testMeta("One")
	tracks.meta["222"] = testMeta("Two")
	docs := newMemDocCache()
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)
	peer := testPeer()

	require.NoError(t, svc.SendTrack(t.Context(), peer, "111"))
	require.NoError(t, svc.SendTrack(t.Context(), peer, "222"))

	digest := sha256Hex(body)
	left := telegram.TelegramAudioCacheKey("111", digest)
	right := telegram.TelegramAudioCacheKey("222", digest)
	require.NotEqual(t, left, right)
	require.Equal(t, 2, sender.uploads)
	require.Zero(t, sender.sends)
	require.Contains(t, docs.items, left)
	require.Contains(t, docs.items, right)
}

func TestSendTrackUploadsAndCachesOnDocumentMiss(t *testing.T) {
	t.Parallel()

	body := []byte("new-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Equal(t, 1, sender.uploads)
	require.Zero(t, sender.sends)
	require.Zero(t, sender.refreshes)

	key := telegram.TelegramAudioCacheKey("123", sha256Hex(body))
	require.Equal(t, sender.uploaded[0], docs.items[key])
	require.NotEmpty(t, docs.items[key].FileReference)
	require.NotZero(t, docs.items[key].MessageID)
}

func TestSendTrackUsesCachedDocumentWithoutUpload(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	cached := sampleDocument(41, []byte{9, 9})
	docs := newMemDocCache()
	key := telegram.TelegramAudioCacheKey("123", sha256Hex(body))
	docs.items[key] = cached
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Zero(t, sender.uploads)
	require.Equal(t, 1, sender.sends)
	require.Zero(t, sender.refreshes)
	require.Equal(t, []int64{cached.ID}, sender.sentIDs)
}

func TestSendTrackCachedSendOrdinaryErrorDoesNotRefreshOrUpload(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	docs.items[telegram.TelegramAudioCacheKey("123", sha256Hex(body))] = sampleDocument(7, []byte{1})
	sender := newMemSender()
	sender.sendErrs = []error{errors.New("network down")}
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.ErrorContains(t, err, "send cached Telegram document")
	require.ErrorContains(t, err, "network down")
	require.Zero(t, sender.uploads)
	require.Zero(t, sender.refreshes)
	require.Equal(t, 1, sender.sends)
}

func TestSendTrackRefreshesExpiredFileReferenceWithoutUpload(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	cached := sampleDocument(7, []byte("old"))
	refreshed := sampleDocument(7, []byte("new"))
	refreshed.AccessHash = 77
	docs := newMemDocCache()
	key := telegram.TelegramAudioCacheKey("123", sha256Hex(body))
	docs.items[key] = cached
	sender := newMemSender()
	sender.sendErrs = []error{tgerr.New(400, "FILE_REFERENCE_0_EXPIRED"), nil}
	sender.refreshed = refreshed
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Zero(t, sender.uploads)
	require.Equal(t, 1, sender.refreshes)
	require.Equal(t, 2, sender.sends)
	require.Equal(t, []int64{cached.ID, refreshed.ID}, sender.sentIDs)
	require.Equal(t, refreshed.FileReference, docs.items[key].FileReference)
	require.Equal(t, refreshed.AccessHash, docs.items[key].AccessHash)
}

func TestSendTrackReuploadsWhenOriginalMessageIsInaccessible(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	key := telegram.TelegramAudioCacheKey("123", sha256Hex(body))
	docs.items[key] = sampleDocument(7, []byte("old"))
	sender := newMemSender()
	sender.sendErrs = []error{tgerr.New(400, "FILE_REFERENCE_EXPIRED")}
	sender.refreshErr = fmt.Errorf("deleted: %w", telegram.ErrMessageNotAccessible)
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Equal(t, 1, sender.refreshes)
	require.Equal(t, 1, sender.uploads)
	require.Equal(t, sender.uploaded[0], docs.items[key])
	require.NotEqual(t, int64(7), docs.items[key].ID)
}

func TestSendTrackRefreshFailureDoesNotReupload(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	docs.items[telegram.TelegramAudioCacheKey("123", sha256Hex(body))] = sampleDocument(7, []byte("old"))
	sender := newMemSender()
	sender.sendErrs = []error{tgerr.New(400, "FILE_REFERENCE_EXPIRED")}
	sender.refreshErr = fmt.Errorf("get message: %w", context.DeadlineExceeded)
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.ErrorContains(t, err, "refresh Telegram document reference")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, sender.refreshes)
	require.Zero(t, sender.uploads)
	require.Equal(t, 1, sender.sends)
}

func TestSendTrackRefreshRetryOrdinaryError(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	docs.items[telegram.TelegramAudioCacheKey("123", sha256Hex(body))] = sampleDocument(7, []byte("old"))
	sender := newMemSender()
	sender.sendErrs = []error{
		tgerr.New(400, "FILE_REFERENCE_EXPIRED"),
		errors.New("peer flooded"),
	}
	sender.refreshed = sampleDocument(7, []byte("fresh"))
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.ErrorContains(t, err, "send refreshed Telegram document")
	require.ErrorContains(t, err, "peer flooded")
	require.Zero(t, sender.uploads)
	require.Equal(t, 2, sender.sends)
	require.Equal(t, 1, sender.refreshes)
}

func TestSendTrackPanicsWhenRefreshedReferenceIsRejected(t *testing.T) {
	t.Parallel()

	body := []byte("cached-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	docs.items[telegram.TelegramAudioCacheKey("123", sha256Hex(body))] = sampleDocument(7, []byte("old"))
	sender := newMemSender()
	sender.sendErrs = []error{
		tgerr.New(400, "FILE_REFERENCE_EXPIRED"),
		tgerr.New(400, "FILE_REFERENCE_INVALID"),
	}
	sender.refreshed = sampleDocument(7, []byte("fresh"))
	svc := telegram.NewTrackService(zerolog.Nop(), newMemMetaCache(), docs, tracks, sender)

	require.PanicsWithValue(t, "file_reference invalid immediately after refresh", func() {
		err := svc.SendTrack(t.Context(), testPeer(), "123")
		require.NoError(t, err)
	})
	require.Zero(t, sender.uploads)
	require.Equal(t, 1, sender.refreshes)
	require.Equal(t, 2, sender.sends)
}

func TestSendTrackDocumentCacheWriteFailureStillSucceeds(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	body := []byte("uploaded-audio")
	tracks := newMemTracks(t, map[string][]byte{"123": body})
	tracks.meta["123"] = testMeta("Song")
	docs := newMemDocCache()
	docs.putErr = errCacheWrite
	sender := newMemSender()
	svc := telegram.NewTrackService(zerolog.New(&logs), newMemMetaCache(), docs, tracks, sender)

	err := svc.SendTrack(t.Context(), testPeer(), "123")
	require.NoError(t, err)
	require.Equal(t, 1, sender.uploads)
	require.Contains(t, logs.String(), "failed to cache uploaded Telegram document")
	require.Contains(t, logs.String(), sha256Hex(body))
	require.NotContains(t, docs.items, telegram.TelegramAudioCacheKey("123", sha256Hex(body)))
}

func TestRefreshedUploadedDocument(t *testing.T) {
	t.Parallel()

	cached := sampleDocument(50, []byte("stale"))
	cached.MessageID = 15

	t.Run("deleted message", func(t *testing.T) {
		t.Parallel()

		_, err := telegram.RefreshedUploadedDocument(cached, messagesResult(&tg.MessageEmpty{ID: 15})) //nolint:exhaustruct_v5
		require.ErrorIs(t, err, telegram.ErrMessageNotAccessible)
	})

	t.Run("empty history", func(t *testing.T) {
		t.Parallel()

		_, err := telegram.RefreshedUploadedDocument(cached, messagesResult())
		require.ErrorIs(t, err, telegram.ErrMessageNotAccessible)
	})

	t.Run("unrelated media", func(t *testing.T) {
		t.Parallel()

		message := &tg.Message{ //nolint:exhaustruct_v5
			ID:    15,
			Media: &tg.MessageMediaEmpty{},
		}
		_, err := telegram.RefreshedUploadedDocument(cached, messagesResult(message))
		require.Error(t, err)
		require.NotErrorIs(t, err, telegram.ErrMessageNotAccessible)
		require.ErrorContains(t, err, "expected document")
	})

	t.Run("different document", func(t *testing.T) {
		t.Parallel()

		message := audioMessage(15, tgDocument(99, []byte("other")))
		_, err := telegram.RefreshedUploadedDocument(cached, messagesResult(message))
		require.Error(t, err)
		require.NotErrorIs(t, err, telegram.ErrMessageNotAccessible)
		require.ErrorContains(t, err, "expected 50")
	})

	t.Run("empty file reference", func(t *testing.T) {
		t.Parallel()

		message := audioMessage(15, tgDocument(50, nil))
		_, err := telegram.RefreshedUploadedDocument(cached, messagesResult(message))
		require.Error(t, err)
		require.NotErrorIs(t, err, telegram.ErrMessageNotAccessible)
		require.ErrorContains(t, err, "empty file reference")
	})

	t.Run("fresh reference", func(t *testing.T) {
		t.Parallel()

		message := audioMessage(15, tgDocument(50, []byte("fresh-ref")))
		got, err := telegram.RefreshedUploadedDocument(cached, messagesResult(message))
		require.NoError(t, err)
		require.Equal(t, int64(50), got.ID)
		require.Equal(t, []byte("fresh-ref"), got.FileReference)
		require.Equal(t, cached.MessageID, got.MessageID)
		require.Equal(t, cached.Peer, got.Peer)
		require.NotEmpty(t, got.FileReference)
	})

	t.Run("not modified", func(t *testing.T) {
		t.Parallel()

		_, err := telegram.RefreshedUploadedDocument(cached, &tg.MessagesMessagesNotModified{Count: 1})
		require.Error(t, err)
		require.NotErrorIs(t, err, telegram.ErrMessageNotAccessible)
	})

	t.Run("different message", func(t *testing.T) {
		t.Parallel()

		message := audioMessage(99, tgDocument(50, []byte("fresh-ref")))
		_, err := telegram.RefreshedUploadedDocument(cached, messagesResult(message))
		require.Error(t, err)
		require.NotErrorIs(t, err, telegram.ErrMessageNotAccessible)
	})
}

func TestUploadedDocumentFromUpdates(t *testing.T) {
	t.Parallel()

	peer := telegram.InputPeer{
		InputPeerClass: &tg.InputPeerChannel{ChannelID: 5, AccessHash: 6},
	}
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 15, RandomID: 1},
			&tg.UpdateNewChannelMessage{
				Message:  audioMessage(15, tgDocument(80, []byte{4, 5, 6})),
				Pts:      3,
				PtsCount: 1,
			},
		},
		Users: nil,
		Chats: nil,
		Date:  10,
		Seq:   11,
	}

	got, err := telegram.UploadedDocumentFromUpdates(updates, peer)
	require.NoError(t, err)
	require.Equal(t, int64(80), got.ID)
	require.Equal(t, []byte{4, 5, 6}, got.FileReference)
	require.Equal(t, 15, got.MessageID)
	require.Equal(t, telegram.PeerKindChannel, got.Peer.Kind)
	require.Equal(t, int64(5), got.Peer.ID)
	require.Equal(t, int64(6), got.Peer.AccessHash)
}

const testCoverPath = "cover.jpg"

var errCacheWrite = errors.New("cache write failed")

type memMetaCache struct {
	items  map[string]types.StoredTrack
	putErr error
	puts   int
}

func newMemMetaCache() *memMetaCache {
	return &memMetaCache{items: map[string]types.StoredTrack{}} //nolint:exhaustruct_v5
}

func (c *memMetaCache) Get(trackID string) (types.StoredTrack, bool) {
	meta, ok := c.items[trackID]
	return meta, ok
}

func (c *memMetaCache) Put(trackID string, meta types.StoredTrack) error {
	c.puts++
	if nil != c.putErr {
		return c.putErr
	}

	c.items[trackID] = meta

	return nil
}

type memDocCache struct {
	items  map[string]telegram.UploadedDocument
	gets   []string
	putErr error
}

func newMemDocCache() *memDocCache {
	return &memDocCache{items: map[string]telegram.UploadedDocument{}} //nolint:exhaustruct_v5
}

func (c *memDocCache) GetUploadedDocument(trackID string, digest string) (telegram.UploadedDocument, bool, error) {
	key := telegram.TelegramAudioCacheKey(trackID, digest)
	c.gets = append(c.gets, key)
	doc, ok := c.items[key]

	return doc, ok, nil
}

func (c *memDocCache) PutUploadedDocument(trackID string, digest string, doc telegram.UploadedDocument) error {
	if nil != c.putErr {
		return c.putErr
	}

	prefix := "telegram-audio:" + trackID + ":"
	key := prefix + digest
	for existing := range c.items {
		if strings.HasPrefix(existing, prefix) && existing != key {
			delete(c.items, existing)
		}
	}
	c.items[key] = doc

	return nil
}

type memTracks struct {
	meta       map[string]types.StoredTrack
	audio      map[string]telegram.TrackAudio
	metaCalls  int
	audioCalls int
}

func newMemTracks(t *testing.T, bodies map[string][]byte) *memTracks {
	t.Helper()

	audio := make(map[string]telegram.TrackAudio, len(bodies))
	for id, body := range bodies {
		audio[id] = telegram.TrackAudio{Path: writeAudio(t, body), CoverPath: testCoverPath}
	}

	return &memTracks{ //nolint:exhaustruct_v5
		meta:  map[string]types.StoredTrack{},
		audio: audio,
	}
}

func (p *memTracks) TrackMeta(ctx context.Context, trackID string) (types.StoredTrack, error) {
	if err := ctx.Err(); nil != err {
		return types.StoredTrack{}, fmt.Errorf("track metadata: %w", err)
	}

	p.metaCalls++
	meta, ok := p.meta[trackID]
	if !ok {
		return types.StoredTrack{}, errors.New("track metadata not found")
	}

	return meta, nil
}

func (p *memTracks) TrackAudio(ctx context.Context, trackID string) (telegram.TrackAudio, error) {
	if err := ctx.Err(); nil != err {
		return telegram.TrackAudio{}, fmt.Errorf("track audio: %w", err)
	}

	p.audioCalls++
	audio, ok := p.audio[trackID]
	if !ok {
		return telegram.TrackAudio{}, errors.New("track audio not found")
	}

	return audio, nil
}

type memSender struct {
	sendErrs   []error
	refreshErr error
	refreshed  telegram.UploadedDocument
	uploads    int
	sends      int
	refreshes  int
	sentIDs    []int64
	uploaded   []telegram.UploadedDocument
	nextID     int64
}

func newMemSender() *memSender {
	return &memSender{nextID: 100} //nolint:exhaustruct_v5
}

func (s *memSender) SendUploadedDocument(
	ctx context.Context,
	logger zerolog.Logger,
	peer telegram.InputPeer,
	uploaded telegram.UploadedDocument,
	track telegram.TrackUpload,
) error {
	if err := ctx.Err(); nil != err {
		return fmt.Errorf("send cached telegram document: %w", err)
	}
	_ = logger
	_ = peer
	_ = track

	s.sends++
	s.sentIDs = append(s.sentIDs, uploaded.ID)
	if len(s.sendErrs) == 0 {
		return nil
	}

	err := s.sendErrs[0]
	s.sendErrs = s.sendErrs[1:]

	return err
}

func (s *memSender) UploadAndSend(
	ctx context.Context,
	logger zerolog.Logger,
	peer telegram.InputPeer,
	track telegram.TrackUpload,
) (telegram.UploadedDocument, error) {
	if err := ctx.Err(); nil != err {
		return telegram.UploadedDocument{}, fmt.Errorf("upload track to Telegram: %w", err)
	}
	_ = logger
	_ = peer
	_ = track

	s.uploads++
	s.nextID++
	doc := sampleDocument(s.nextID, fmt.Appendf(nil, "ref-%d", s.nextID))
	s.uploaded = append(s.uploaded, doc)

	return doc, nil
}

func (s *memSender) RefreshUploadedDocument(
	ctx context.Context,
	logger zerolog.Logger,
	uploaded telegram.UploadedDocument,
) (telegram.UploadedDocument, error) {
	if err := ctx.Err(); nil != err {
		return telegram.UploadedDocument{}, fmt.Errorf("refresh Telegram document reference: %w", err)
	}
	_ = logger
	_ = uploaded

	s.refreshes++
	if nil != s.refreshErr {
		return telegram.UploadedDocument{}, s.refreshErr
	}

	return s.refreshed, nil
}

func (s *memSender) IsFileReferenceError(err error) bool {
	return telegram.IsFileReferenceError(err)
}

func (s *memSender) IsMessageNotAccessible(err error) bool {
	return telegram.IsMessageNotAccessible(err)
}

func sampleDocument(id int64, ref []byte) telegram.UploadedDocument {
	return telegram.UploadedDocument{
		ID:            id,
		AccessHash:    id + 10,
		FileReference: ref,
		Peer: telegram.MessagePeer{
			Kind:       telegram.PeerKindUser,
			ID:         7,
			AccessHash: 8,
		},
		MessageID: 42,
	}
}

func testPeer() telegram.InputPeer {
	return telegram.InputPeer{
		InputPeerClass: &tg.InputPeerUser{UserID: 7, AccessHash: 8},
	}
}

func testMeta(title string) types.StoredTrack {
	return types.StoredTrack{
		Track: types.Track{
			Artists: []types.TrackArtist{{
				Name: "Artist",
				Type: types.ArtistTypeMain,
			}},
			Title:        title,
			Duration:     12,
			TrackNumber:  1,
			VolumeNumber: 1,
			Version:      nil,
			CoverID:      "cover-id",
			Ext:          "flac",
			Quality:      "FLAC",
		},
		AlbumTitle:  "Album",
		ReleaseDate: time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
	}
}

func writeAudio(t *testing.T, body []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "audio.bin")
	require.NoError(t, os.WriteFile(path, body, 0o600))

	return path
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func messagesResult(messages ...tg.MessageClass) *tg.MessagesMessages {
	return &tg.MessagesMessages{
		Messages: messages,
		Topics:   nil,
		Chats:    nil,
		Users:    nil,
	}
}

func audioMessage(id int, doc *tg.Document) *tg.Message {
	return &tg.Message{ //nolint:exhaustruct_v5
		ID: id,
		Media: &tg.MessageMediaDocument{ //nolint:exhaustruct_v5
			Document: doc,
		},
	}
}

func tgDocument(id int64, ref []byte) *tg.Document {
	return &tg.Document{ //nolint:exhaustruct_v5
		ID:            id,
		AccessHash:    id + 1,
		FileReference: ref,
	}
}
