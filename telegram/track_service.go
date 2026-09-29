package telegram

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/tidal/types"
)

// TrackMetaCache stores track metadata separately from Telegram media.
// Keys are track:<trackID>:metadata. A write failure is not fatal.
type TrackMetaCache interface {
	Get(trackID string) (types.StoredTrack, bool)
	Put(trackID string, meta types.StoredTrack) error
}

// UploadedDocumentCache stores one Telegram document per track and content
// digest. Put replaces that digest and deletes every other digest for the
// track. A write failure is not fatal after a successful send.
type UploadedDocumentCache interface {
	GetUploadedDocument(trackID string, digest string) (UploadedDocument, bool, error)
	PutUploadedDocument(trackID string, digest string, doc UploadedDocument) error
}

// TrackAudio is the on-disk audio that would be uploaded, plus its cover.
type TrackAudio struct {
	Path      string
	CoverPath string
}

// TrackProvider loads track metadata and the final audio file.
type TrackProvider interface {
	TrackMeta(ctx context.Context, trackID string) (types.StoredTrack, error)
	TrackAudio(ctx context.Context, trackID string) (TrackAudio, error)
}

// TrackUpload is one track's metadata and files, used to send or upload audio.
type TrackUpload struct {
	ID        string
	AudioPath string
	CoverPath string
	Info      types.StoredTrack
}

// TelegramSender uploads audio, resends a cached document, and refreshes an
// expired file reference from the message that contains it.
type TelegramSender interface {
	SendUploadedDocument(
		ctx context.Context,
		logger zerolog.Logger,
		peer InputPeer,
		uploaded UploadedDocument,
		track TrackUpload,
	) error
	UploadAndSend(
		ctx context.Context,
		logger zerolog.Logger,
		peer InputPeer,
		track TrackUpload,
	) (UploadedDocument, error)
	RefreshUploadedDocument(
		ctx context.Context,
		logger zerolog.Logger,
		uploaded UploadedDocument,
	) (UploadedDocument, error)
	IsFileReferenceError(err error) bool
	IsMessageNotAccessible(err error) bool
}

// TrackService sends a downloaded track to Telegram, reusing a previous
// upload when the audio bytes are unchanged.
type TrackService struct {
	logger zerolog.Logger
	meta   TrackMetaCache
	docs   UploadedDocumentCache
	tracks TrackProvider
	sender TelegramSender
}

var (
	_ TrackMetaCache        = (*memoryTrackMetaCache)(nil)
	_ UploadedDocumentCache = (*Storage)(nil)
	_ TrackProvider         = (*downloadsTrackProvider)(nil)
	_ TelegramSender        = (*Uploader)(nil)
)

func NewTrackService(
	logger zerolog.Logger,
	meta TrackMetaCache,
	docs UploadedDocumentCache,
	tracks TrackProvider,
	sender TelegramSender,
) *TrackService {
	return &TrackService{
		logger: logger,
		meta:   meta,
		docs:   docs,
		tracks: tracks,
		sender: sender,
	}
}

func (s *TrackService) SendTrack(ctx context.Context, peer InputPeer, trackID string) error {
	if len(trackID) == 0 {
		return errors.New("track id is empty")
	}
	if nil == peer.InputPeerClass {
		return errors.New("telegram peer is empty")
	}

	logger := s.logger.With().Str("track_id", trackID).Logger()

	meta, ok := s.meta.Get(trackID)
	if !ok {
		fetched, err := s.tracks.TrackMeta(ctx, trackID)
		if nil != err {
			return fmt.Errorf("fetch track metadata: %w", err)
		}

		meta = fetched
		if err := s.meta.Put(trackID, meta); nil != err {
			logger.Warn().Err(err).Msg("failed to cache track metadata")
		}
	}

	audio, err := s.tracks.TrackAudio(ctx, trackID)
	if nil != err {
		return fmt.Errorf("download track audio: %w", err)
	}

	digest, err := FileSHA256(ctx, logger, audio.Path)
	if nil != err {
		return fmt.Errorf("hash track audio: %w", err)
	}

	cacheKey := TelegramAudioCacheKey(trackID, digest)
	track := TrackUpload{
		ID:        trackID,
		AudioPath: audio.Path,
		CoverPath: audio.CoverPath,
		Info:      meta,
	}

	uploaded, ok, err := s.docs.GetUploadedDocument(trackID, digest)
	if nil != err {
		return fmt.Errorf("read cached Telegram document: %w", err)
	}
	if !ok {
		// The bot runs one download job at a time, so a miss here is not single-flighted.
		return s.uploadAndRemember(ctx, logger, peer, track, cacheKey, digest)
	}

	err = s.sender.SendUploadedDocument(ctx, logger, peer, uploaded, track)
	if nil == err {
		return nil
	}
	if !s.sender.IsFileReferenceError(err) {
		return fmt.Errorf("send cached Telegram document: %w", err)
	}

	refreshed, err := s.sender.RefreshUploadedDocument(ctx, logger, uploaded)
	if nil != err {
		if !s.sender.IsMessageNotAccessible(err) {
			return fmt.Errorf("refresh Telegram document reference: %w", err)
		}

		return s.uploadAndRemember(ctx, logger, peer, track, cacheKey, digest)
	}

	if err := s.docs.PutUploadedDocument(trackID, digest, refreshed); nil != err {
		logger.Warn().
			Str("content_sha256", digest).
			Str("cache_key", cacheKey).
			Int64("telegram_document_id", refreshed.ID).
			Int("telegram_message_id", refreshed.MessageID).
			Err(err).
			Msg("failed to cache uploaded Telegram document")
	}

	if err := s.sender.SendUploadedDocument(ctx, logger, peer, refreshed, track); nil != err {
		if s.sender.IsFileReferenceError(err) {
			panic("file_reference invalid immediately after refresh")
		}

		return fmt.Errorf("send refreshed Telegram document: %w", err)
	}

	return nil
}

func (s *TrackService) uploadAndRemember(
	ctx context.Context,
	logger zerolog.Logger,
	peer InputPeer,
	track TrackUpload,
	cacheKey string,
	digest string,
) error {
	uploaded, err := s.sender.UploadAndSend(ctx, logger, peer, track)
	if nil != err {
		return fmt.Errorf("upload track to Telegram: %w", err)
	}

	if err := s.docs.PutUploadedDocument(track.ID, digest, uploaded); nil != err {
		logger.Warn().
			Str("content_sha256", digest).
			Str("cache_key", cacheKey).
			Int64("telegram_document_id", uploaded.ID).
			Int("telegram_message_id", uploaded.MessageID).
			Err(err).
			Msg("failed to cache uploaded Telegram document")
	}

	return nil
}
