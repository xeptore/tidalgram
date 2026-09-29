package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/telegram/progress"
	"github.com/xeptore/tidalgram/tidal/types"
)

func (u *Uploader) IsFileReferenceError(err error) bool {
	return IsFileReferenceError(err)
}

func (u *Uploader) IsMessageNotAccessible(err error) bool {
	return IsMessageNotAccessible(err)
}

func (u *Uploader) SendUploadedDocument(
	ctx context.Context,
	logger zerolog.Logger,
	peer InputPeer,
	uploaded UploadedDocument,
	track TrackUpload,
) error {
	caption := trackCaption(track, u.conf.Upload.Signature)
	input := &tg.InputDocument{
		ID:            uploaded.ID,
		AccessHash:    uploaded.AccessHash,
		FileReference: bytes.Clone(uploaded.FileReference),
	}
	media := message.Document(input, caption...)

	_, err := message.
		NewSender(u.client).
		To(peer).
		Clear().
		Background().
		Silent().
		Media(ctx, media)
	if nil != err {
		if !IsFileReferenceError(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.Error().
				Err(err).
				Int64("telegram_document_id", uploaded.ID).
				Int("telegram_message_id", uploaded.MessageID).
				Msg("Failed to send cached Telegram document")
		}

		return fmt.Errorf("send cached telegram document: %w", err)
	}

	time.Sleep(u.conf.Upload.PauseDuration.Duration)

	return nil
}

func (u *Uploader) UploadAndSend(
	ctx context.Context,
	logger zerolog.Logger,
	peer InputPeer,
	track TrackUpload,
) (UploadedDocument, error) {
	if _, err := messagePeerFrom(peer); nil != err {
		return UploadedDocument{}, fmt.Errorf("capture telegram peer: %w", err)
	}

	trackStat, err := os.Lstat(track.AudioPath)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to stat track file")
		return UploadedDocument{}, fmt.Errorf("stat track file: %w", err)
	}
	if !trackStat.Mode().IsRegular() {
		return UploadedDocument{}, fmt.Errorf("track file %q is not a regular file", track.AudioPath)
	}
	if trackStat.Size() == 0 {
		return UploadedDocument{}, errors.New("track file is empty")
	}

	coverStat, err := os.Lstat(track.CoverPath)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to stat track cover file")
		return UploadedDocument{}, fmt.Errorf("stat track cover file: %w", err)
	}
	if !coverStat.Mode().IsRegular() {
		return UploadedDocument{}, fmt.Errorf("track cover file %q is not a regular file", track.CoverPath)
	}
	if coverStat.Size() == 0 {
		return UploadedDocument{}, errors.New("track cover file is empty")
	}

	trackProgress := &progress.Track{Size: trackStat.Size()}
	coverProgress := &progress.Cover{Size: coverStat.Size()}
	monitor := progress.NewTrackMonitor(coverProgress, trackProgress)

	typingWait := make(chan struct{})
	go u.keepTyping(ctx, monitor, typingWait, logger)

	trackInputFile, err := u.newUploader().WithProgress(trackProgress).FromPath(ctx, track.AudioPath)
	if nil != err {
		logUnexpected(logger, err, "Failed to upload track file")
		return UploadedDocument{}, fmt.Errorf("upload track file: %w", err)
	}

	coverInputFile, err := u.newUploader().WithProgress(coverProgress).FromPath(ctx, track.CoverPath)
	if nil != err {
		logUnexpected(logger, err, "Failed to upload track cover file")
		return UploadedDocument{}, fmt.Errorf("upload track cover file: %w", err)
	}

	select {
	case <-typingWait:
	case <-ctx.Done():
		return UploadedDocument{}, fmt.Errorf("wait for typing: %w", ctx.Err())
	}

	mime, err := mimetype.DetectFile(track.AudioPath)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to detect track mime")
		return UploadedDocument{}, fmt.Errorf("detect mime: %w", err)
	}

	media := message.
		UploadedDocument(trackInputFile, trackCaption(track, u.conf.Upload.Signature)...).
		MIME(mime.String()).
		Attributes(
			&tg.DocumentAttributeFilename{
				FileName: track.Info.UploadFilename(),
			},
			//nolint:exhaustruct_v5
			&tg.DocumentAttributeAudio{
				Title:     track.Info.Title,
				Performer: types.JoinArtists(track.Info.Artists),
				Duration:  track.Info.Duration,
			}).
		Thumb(coverInputFile).
		Audio().
		DurationSeconds(track.Info.Duration).
		Performer(types.JoinArtists(track.Info.Artists)).
		Title(track.Info.Title)

	updates, err := message.
		NewSender(u.client).
		To(peer).
		Clear().
		Background().
		Silent().
		Media(ctx, media)
	if nil != err {
		logUnexpected(logger, err, "Failed to send track message")
		return UploadedDocument{}, fmt.Errorf("send message: %w", err)
	}

	time.Sleep(u.conf.Upload.PauseDuration.Duration)

	uploaded, err := UploadedDocumentFromUpdates(updates, peer)
	if nil != err {
		return UploadedDocument{}, fmt.Errorf("read sent telegram document: %w", err)
	}

	return uploaded, nil
}

func (u *Uploader) RefreshUploadedDocument(
	ctx context.Context,
	logger zerolog.Logger,
	uploaded UploadedDocument,
) (UploadedDocument, error) {
	result, err := u.fetchUploadedMessage(ctx, logger, uploaded)
	if nil != err {
		return UploadedDocument{}, err
	}

	refreshed, err := RefreshedUploadedDocument(uploaded, result)
	if nil != err {
		return UploadedDocument{}, err
	}

	return refreshed, nil
}

func (u *Uploader) fetchUploadedMessage(
	ctx context.Context,
	logger zerolog.Logger,
	uploaded UploadedDocument,
) (tg.MessagesMessagesClass, error) {
	ids := []tg.InputMessageClass{
		&tg.InputMessageID{ID: uploaded.MessageID},
	}

	switch uploaded.Peer.Kind {
	case PeerKindChannel:
		result, err := u.client.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{
				ChannelID:  uploaded.Peer.ID,
				AccessHash: uploaded.Peer.AccessHash,
			},
			ID: ids,
		})
		if nil != err {
			logMessageLoadError(logger, err, uploaded)
			return nil, fmt.Errorf("get channel message: %w", err)
		}

		return result, nil
	case PeerKindUser, PeerKindChat:
		result, err := u.client.MessagesGetMessages(ctx, ids)
		if nil != err {
			logMessageLoadError(logger, err, uploaded)
			return nil, fmt.Errorf("get message: %w", err)
		}

		return result, nil
	default:
		return nil, fmt.Errorf("unsupported telegram peer kind %d", uploaded.Peer.Kind)
	}
}

func logUnexpected(logger zerolog.Logger, err error, msg string) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}

	logger.Error().Err(err).Msg(msg)
}

func logMessageLoadError(logger zerolog.Logger, err error, uploaded UploadedDocument) {
	if IsMessageNotAccessible(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}

	logger.Error().
		Err(err).
		Int64("telegram_document_id", uploaded.ID).
		Int("telegram_message_id", uploaded.MessageID).
		Msg("Failed to load Telegram message for document refresh")
}

func trackCaption(track TrackUpload, signature string) []message.StyledTextOption {
	return songCaption(
		track.Info.AlbumTitle,
		track.Info.ReleaseDate,
		track.Info.Quality,
		track.Info.VolumeNumber,
		track.Info.TrackNumber,
		track.ID,
		signature,
	)
}
