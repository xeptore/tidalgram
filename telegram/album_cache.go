package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/telegram/progress"
	"github.com/xeptore/tidalgram/tidal/types"
)

func storedAlbumTrack(
	id string,
	audioPath string,
	coverPath string,
	info *types.StoredTrack,
	trackProgress *progress.Track,
	coverProgress *progress.Cover,
) albumTrack {
	return albumTrack{
		id:            id,
		audioPath:     audioPath,
		coverPath:     coverPath,
		filename:      info.UploadFilename(),
		title:         info.Title,
		performer:     types.JoinArtists(info.Artists),
		duration:      info.Duration,
		albumTitle:    info.AlbumTitle,
		releaseDate:   info.ReleaseDate,
		quality:       info.Quality,
		volumeNumber:  info.VolumeNumber,
		trackNumber:   info.TrackNumber,
		trackProgress: trackProgress,
		coverProgress: coverProgress,
	}
}

// albumTrack is one audio file inside an album, playlist, mix, or credit batch.
type albumTrack struct {
	id            string
	audioPath     string
	coverPath     string
	filename      string
	title         string
	performer     string
	duration      int
	albumTitle    string
	releaseDate   time.Time
	quality       string
	volumeNumber  int
	trackNumber   int
	trackProgress *progress.Track
	coverProgress *progress.Cover
}

type albumSlot struct {
	index          int
	track          albumTrack
	digest         string
	cached         *UploadedDocument
	media          message.MultiMediaOption
	cacheAfterSend bool
}

type sentAlbumDocument struct {
	text string
	doc  UploadedDocument
}

// ContainsTrackID reports whether text contains trackID as its own numeric token.
// Album captions include the track ID, which is how sent messages are matched
// back to the files that were uploaded.
func ContainsTrackID(text string, trackID string) bool {
	if len(trackID) == 0 || len(text) < len(trackID) {
		return false
	}

	from := 0
	for {
		rel := strings.Index(text[from:], trackID)
		if rel < 0 {
			return false
		}

		at := from + rel
		end := at + len(trackID)
		beforeDigit := at > 0 && isASCIIDigit(text[at-1])
		afterDigit := end < len(text) && isASCIIDigit(text[end])
		if !beforeDigit && !afterDigit {
			return true
		}

		from = at + 1
	}
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func (u *Uploader) sendAlbumTracks(
	ctx context.Context,
	logger zerolog.Logger,
	tracks []albumTrack,
	monitor progress.Monitor,
	loadSharedCover func(context.Context) (tg.InputFileClass, error),
) error {
	if len(tracks) == 0 {
		return nil
	}

	slots := make([]albumSlot, len(tracks))
	needsUpload := false
	for i := range tracks {
		trackLogger := logger.With().Int("index", i).Str("track_id", tracks[i].id).Logger()
		digest, err := FileSHA256(ctx, trackLogger, tracks[i].audioPath)
		if nil != err {
			return fmt.Errorf("hash track audio: %w", err)
		}

		uploaded, ok, err := u.docCache.GetUploadedDocument(tracks[i].id, digest)
		if nil != err {
			return fmt.Errorf("read cached Telegram document: %w", err)
		}

		slots[i] = albumSlot{
			index:          i,
			track:          tracks[i],
			digest:         digest,
			cached:         nil,
			media:          nil,
			cacheAfterSend: false,
		}
		if !ok {
			needsUpload = true
			continue
		}

		doc := uploaded
		slots[i].cached = &doc
		tracks[i].trackProgress.Finish()
		if nil != tracks[i].coverProgress {
			tracks[i].coverProgress.Finish()
		}
	}

	var sharedCover tg.InputFileClass
	if needsUpload && nil != loadSharedCover {
		cover, err := loadSharedCover(ctx)
		if nil != err {
			return err
		}
		sharedCover = cover
	}

	typingWait := make(chan struct{})
	go u.keepTyping(ctx, monitor, typingWait, logger)

	if err := u.prepareAlbumSlots(ctx, logger, slots, sharedCover); nil != err {
		return err
	}

	if err := u.dispatchAlbum(ctx, logger, slots, loadSharedCover); nil != err {
		return err
	}

	select {
	case <-typingWait:
		time.Sleep(u.conf.Upload.PauseDuration.Duration)
	case <-ctx.Done():
		return fmt.Errorf("wait for typing: %w", ctx.Err())
	}

	return nil
}

func (u *Uploader) prepareAlbumSlots(
	ctx context.Context,
	logger zerolog.Logger,
	slots []albumSlot,
	sharedCover tg.InputFileClass,
) error {
	wg, wgctx := errgroup.WithContext(ctx)
	wg.SetLimit(u.conf.Upload.Limit)

	for i := range slots {
		if nil != slots[i].cached {
			slots[i].media = u.cachedAlbumMedia(slots[i])
			continue
		}

		wg.Go(func() error {
			if err := wgctx.Err(); nil != err {
				return fmt.Errorf("upload album track: %w", err)
			}

			media, err := u.freshAlbumMedia(wgctx, logger, &slots[i], sharedCover)
			if nil != err {
				return err
			}

			slots[i].media = media
			slots[i].cacheAfterSend = true

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("upload album tracks: %w", err)
	}

	return nil
}

func (u *Uploader) dispatchAlbum(
	ctx context.Context,
	logger zerolog.Logger,
	slots []albumSlot,
	loadSharedCover func(context.Context) (tg.InputFileClass, error),
) error {
	updates, err := u.sendAlbumSlots(ctx, slots)
	if nil != err {
		if !IsFileReferenceError(err) || !albumUsesCachedDocument(slots) {
			logUnexpected(logger, err, "Failed to send album")
			return err
		}

		if err := u.refreshAlbumSlots(ctx, logger, slots, loadSharedCover); nil != err {
			return err
		}

		updates, err = u.sendAlbumSlots(ctx, slots)
		if nil != err {
			if IsFileReferenceError(err) {
				panic("file_reference invalid immediately after refresh")
			}

			logUnexpected(logger, err, "Failed to send album")

			return err
		}
	}

	u.rememberAlbumUploads(logger, slots, updates)

	return nil
}

func (u *Uploader) refreshAlbumSlots(
	ctx context.Context,
	logger zerolog.Logger,
	slots []albumSlot,
	loadSharedCover func(context.Context) (tg.InputFileClass, error),
) error {
	for i := range slots {
		if nil == slots[i].cached {
			continue
		}

		trackLogger := logger.With().Int("index", i).Str("track_id", slots[i].track.id).Logger()
		refreshed, err := u.RefreshUploadedDocument(ctx, trackLogger, *slots[i].cached)
		if nil != err {
			if !IsMessageNotAccessible(err) {
				return fmt.Errorf("refresh Telegram document reference: %w", err)
			}

			var sharedCover tg.InputFileClass
			if len(slots[i].track.coverPath) == 0 && nil != loadSharedCover {
				sharedCover, err = loadSharedCover(ctx)
				if nil != err {
					return err
				}
			}

			media, err := u.freshAlbumMedia(ctx, logger, &slots[i], sharedCover)
			if nil != err {
				return err
			}

			slots[i].cached = nil
			slots[i].media = media
			slots[i].cacheAfterSend = true

			continue
		}

		if err := u.docCache.PutUploadedDocument(slots[i].track.id, slots[i].digest, refreshed); nil != err {
			trackLogger.Warn().
				Str("content_sha256", slots[i].digest).
				Str("cache_key", TelegramAudioCacheKey(slots[i].track.id, slots[i].digest)).
				Int64("telegram_document_id", refreshed.ID).
				Int("telegram_message_id", refreshed.MessageID).
				Err(err).
				Msg("failed to cache uploaded Telegram document")
		}

		doc := refreshed
		slots[i].cached = &doc
		slots[i].media = u.cachedAlbumMedia(slots[i])
		slots[i].cacheAfterSend = false
	}

	return nil
}

func (u *Uploader) rememberAlbumUploads(logger zerolog.Logger, slots []albumSlot, updates tg.UpdatesClass) {
	sent, err := sentAlbumDocuments(updates, u.peer)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read sent album documents")
		return
	}

	used := make([]bool, len(sent))
	for i := range slots {
		if !slots[i].cacheAfterSend {
			continue
		}

		trackLogger := logger.With().Int("index", i).Str("track_id", slots[i].track.id).Logger()
		doc, ok := takeAlbumDocument(sent, used, slots[i].track.id)
		if !ok {
			trackLogger.Warn().
				Str("content_sha256", slots[i].digest).
				Str("cache_key", TelegramAudioCacheKey(slots[i].track.id, slots[i].digest)).
				Msg("failed to cache uploaded Telegram document")

			continue
		}

		if err := u.docCache.PutUploadedDocument(slots[i].track.id, slots[i].digest, doc); nil != err {
			trackLogger.Warn().
				Str("content_sha256", slots[i].digest).
				Str("cache_key", TelegramAudioCacheKey(slots[i].track.id, slots[i].digest)).
				Int64("telegram_document_id", doc.ID).
				Int("telegram_message_id", doc.MessageID).
				Err(err).
				Msg("failed to cache uploaded Telegram document")
		}
	}
}

func (u *Uploader) sendAlbumSlots(ctx context.Context, slots []albumSlot) (tg.UpdatesClass, error) {
	media := make([]message.MultiMediaOption, len(slots))
	for i := range slots {
		media[i] = slots[i].media
	}

	var rest []message.MultiMediaOption
	if len(media) > 1 {
		rest = media[1:]
	}

	updates, err := message.
		NewSender(u.client).
		To(u.peer).
		Clear().
		Background().
		Silent().
		Album(ctx, media[0], rest...)
	if nil != err {
		return nil, fmt.Errorf("send album: %w", err)
	}

	return updates, nil
}

func (u *Uploader) freshAlbumMedia(
	ctx context.Context,
	logger zerolog.Logger,
	slot *albumSlot,
	sharedCover tg.InputFileClass,
) (message.MultiMediaOption, error) {
	trackLogger := logger.With().Int("index", slot.index).Str("track_id", slot.track.id).Logger()

	trackInput, err := u.newUploader().WithProgress(slot.track.trackProgress).FromPath(ctx, slot.track.audioPath)
	if nil != err {
		logUnexpected(trackLogger, err, "Failed to upload track file")
		return nil, fmt.Errorf("upload track file: %w", err)
	}

	coverInput := sharedCover
	if nil == coverInput {
		if len(slot.track.coverPath) == 0 || nil == slot.track.coverProgress {
			return nil, errors.New("track cover file is missing")
		}

		coverInput, err = u.newUploader().WithProgress(slot.track.coverProgress).FromPath(ctx, slot.track.coverPath)
		if nil != err {
			logUnexpected(trackLogger, err, "Failed to upload track cover file")
			return nil, fmt.Errorf("upload track cover file: %w", err)
		}
	}

	mime, err := mimetype.DetectFile(slot.track.audioPath)
	if nil != err {
		trackLogger.Error().Err(err).Msg("Failed to detect track mime")
		return nil, fmt.Errorf("detect mime: %w", err)
	}

	caption := albumTrackCaption(slot.track, u.conf.Upload.Signature)
	doc := message.
		UploadedDocument(trackInput, caption...).
		MIME(mime.String()).
		Attributes(
			&tg.DocumentAttributeFilename{
				FileName: slot.track.filename,
			},
			//nolint:exhaustruct_v5
			&tg.DocumentAttributeAudio{
				Title:     slot.track.title,
				Performer: slot.track.performer,
				Duration:  slot.track.duration,
			}).
		Thumb(coverInput).
		Audio().
		DurationSeconds(slot.track.duration).
		Performer(slot.track.performer).
		Title(slot.track.title)

	return doc, nil
}

func (u *Uploader) cachedAlbumMedia(slot albumSlot) message.MultiMediaOption {
	input := &tg.InputDocument{
		ID:            slot.cached.ID,
		AccessHash:    slot.cached.AccessHash,
		FileReference: bytes.Clone(slot.cached.FileReference),
	}

	return message.Document(input, albumTrackCaption(slot.track, u.conf.Upload.Signature)...)
}

func albumTrackCaption(track albumTrack, signature string) []message.StyledTextOption {
	return songCaption(
		track.albumTitle,
		track.releaseDate,
		track.quality,
		track.volumeNumber,
		track.trackNumber,
		track.id,
		signature,
	)
}

func albumUsesCachedDocument(slots []albumSlot) bool {
	for i := range slots {
		if nil != slots[i].cached {
			return true
		}
	}

	return false
}

func sentAlbumDocuments(updates tg.UpdatesClass, peer InputPeer) ([]sentAlbumDocument, error) {
	list, err := updateList(updates)
	if nil != err {
		return nil, err
	}

	messagePeer, err := messagePeerFrom(peer)
	if nil != err {
		return nil, err
	}

	sent := make([]sentAlbumDocument, 0, len(list))
	for _, upd := range list {
		message, ok, err := messageFromUpdate(upd)
		if nil != err {
			return nil, err
		}
		if !ok {
			continue
		}

		if _, isDocument := message.Media.(*tg.MessageMediaDocument); !isDocument {
			continue
		}

		document, err := documentFromMessage(message)
		if nil != err {
			return nil, err
		}

		uploaded, err := uploadedDocumentFrom(document, message.ID, messagePeer)
		if nil != err {
			return nil, err
		}

		sent = append(sent, sentAlbumDocument{
			text: message.Message,
			doc:  uploaded,
		})
	}

	return sent, nil
}

func takeAlbumDocument(sent []sentAlbumDocument, used []bool, trackID string) (UploadedDocument, bool) {
	for i := range sent {
		if used[i] {
			continue
		}
		if !ContainsTrackID(sent[i].text, trackID) {
			continue
		}

		used[i] = true

		return sent[i].doc, true
	}

	return UploadedDocument{}, false //nolint:exhaustruct_v5
}
