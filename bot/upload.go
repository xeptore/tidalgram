package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/mathutil"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

func uploadAlbum(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	dir fs.DownloadsDir,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	id string,
) error {
	albumFs := dir.Album(id)

	info, err := albumFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("failed to read album info file: %v", err)
	}

	for volIdx, trackIDs := range info.VolumeTrackIDs {
		var (
			volNum       = volIdx + 1
			batchSize    = mathutil.OptimalAlbumSize(len(trackIDs))
			numBatches   = mathutil.DivCeil(len(trackIDs), batchSize)
			batches      = slices.Collect(slices.Chunk(trackIDs, batchSize))
			uploadMedias = make([]UploadMedia, 0, len(trackIDs))
		)
		for i, trackIDs := range batches {
			caption := strings.Join(
				[]string{
					info.Caption,
					"",
					fmt.Sprintf("_Volume: %d_", volNum),
					fmt.Sprintf("_Part: %d/%d_", i+1, numBatches),
				},
				"\n",
			)

			for _, trackID := range trackIDs {
				trackFs := albumFs.Track(volNum, trackID)
				trackInfo, err := trackFs.InfoFile.Read()
				if nil != err {
					return fmt.Errorf("failed to read album track info file: %v", err)
				}

				uploadMedias = append(uploadMedias, UploadMedia{
					TrackFilename: trackInfo.UploadFilename(),
					TrackPath:     trackFs.Path,
					CoverPath:     albumFs.Cover.Path,
					Title:         trackInfo.UploadTitle(),
					Performer:     types.JoinArtists(trackInfo.Artists),
					Duration:      trackInfo.Duration,
				})
			}

			if err := uploadTracksBatch(ctx, logger, b, conf, chatID, replyMessageID, uploadMedias, caption); nil != err {
				return fmt.Errorf("failed to upload album tracks batch: %w", err)
			}
		}
	}

	return nil
}

type UploadMedia struct {
	TrackFilename string
	TrackPath     string
	CoverPath     string
	Title         string
	Performer     string
	Duration      int
}

func uploadPlaylist(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	dir fs.DownloadsDir,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	id string,
) error {
	playlistFs := dir.Playlist(id)

	info, err := playlistFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("failed to read playlist info file: %v", err)
	}

	var (
		batchSize    = mathutil.OptimalAlbumSize(len(info.TrackIDs))
		batches      = slices.Collect(slices.Chunk(info.TrackIDs, batchSize))
		numBatches   = mathutil.DivCeil(len(info.TrackIDs), batchSize)
		uploadMedias = make([]UploadMedia, 0, len(info.TrackIDs))
	)
	for i, trackIDs := range batches {
		caption := strings.Join(
			[]string{
				info.Caption,
				"",
				fmt.Sprintf("_Part: %d/%d_", i+1, numBatches),
			},
			"\n",
		)

		for _, trackID := range trackIDs {
			trackFs := playlistFs.Track(trackID)
			trackInfo, err := trackFs.InfoFile.Read()
			if nil != err {
				return fmt.Errorf("failed to read playlist track info file: %v", err)
			}

			uploadMedias = append(uploadMedias, UploadMedia{
				TrackFilename: trackInfo.UploadFilename(),
				TrackPath:     trackFs.Path,
				CoverPath:     trackFs.Cover.Path,
				Title:         trackInfo.UploadTitle(),
				Performer:     types.JoinArtists(trackInfo.Artists),
				Duration:      trackInfo.Duration,
			})
		}

		if err := uploadTracksBatch(ctx, logger, b, conf, chatID, replyMessageID, uploadMedias, caption); nil != err {
			return fmt.Errorf("failed to upload playlist tracks batch: %w", err)
		}
	}

	return nil
}

func uploadMix(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	dir fs.DownloadsDir,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	id string,
) error {
	mixFs := dir.Mix(id)

	info, err := mixFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("failed to read mix info file: %v", err)
	}

	var (
		batchSize    = mathutil.OptimalAlbumSize(len(info.TrackIDs))
		batches      = slices.Collect(slices.Chunk(info.TrackIDs, batchSize))
		numBatches   = mathutil.DivCeil(len(info.TrackIDs), batchSize)
		uploadMedias = make([]UploadMedia, 0, len(info.TrackIDs))
	)
	for i, trackIDs := range batches {
		caption := strings.Join(
			[]string{
				info.Caption,
				"",
				fmt.Sprintf("_Part: %d/%d_", i+1, numBatches),
			},
			"\n",
		)

		for _, trackID := range trackIDs {
			trackFs := mixFs.Track(trackID)
			trackInfo, err := trackFs.InfoFile.Read()
			if nil != err {
				return fmt.Errorf("failed to read mix track info file: %v", err)
			}

			uploadMedias = append(uploadMedias, UploadMedia{
				TrackFilename: trackInfo.UploadFilename(),
				TrackPath:     trackFs.Path,
				CoverPath:     trackFs.Cover.Path,
				Title:         trackInfo.UploadTitle(),
				Performer:     types.JoinArtists(trackInfo.Artists),
				Duration:      trackInfo.Duration,
			})
		}

		if err := uploadTracksBatch(ctx, logger, b, conf, chatID, replyMessageID, uploadMedias, caption); nil != err {
			return fmt.Errorf("failed to upload mix tracks batch: %w", err)
		}
	}

	return nil
}

func uploadTracksBatch(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	medias []UploadMedia,
	caption string,
) (err error) {
	var (
		closers    = make([]func() error, 0, len(medias))
		trackMedia = make([]gotgbot.InputMedia, 0, len(medias))
	)

	defer func() {
		for _, closer := range closers {
			if closeErr := closer(); nil != closeErr {
				err = errors.Join(err, fmt.Errorf("failed to close track file: %v", closeErr))
			}
		}
	}()

	for i, media := range medias {
		logger := logger.With().Int("index", i).Logger()

		trackFile, err := os.Open(media.TrackPath)
		if nil != err {
			logger.Error().Err(err).Msg("failed to open track file")
			return fmt.Errorf("failed to open track file: %v", err)
		}

		closers = append(closers, func() error {
			if err := trackFile.Close(); nil != err {
				logger.Error().Err(err).Msg("failed to close track file")
				return fmt.Errorf("failed to close track file: %v", err)
			}

			return nil
		})

		inputMedia := gotgbot.InputMediaAudio{
			Media:           gotgbot.InputFileByReader(media.TrackFilename, trackFile),
			Title:           media.Title,
			Performer:       media.Performer,
			Duration:        int64(media.Duration),
			Thumbnail:       gotgbot.InputFileByReader(media.CoverPath, trackFile),
			Caption:         "",
			ParseMode:       "",
			CaptionEntities: nil,
		}

		if i == len(medias)-1 {
			inputMedia.Caption = strings.Join([]string{caption, "", conf.Signature}, "\n")
		}

		trackMedia = append(trackMedia, inputMedia)
	}

	sendOpts := &gotgbot.SendMediaGroupOpts{ //nolint:exhaustruct
		ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
			MessageId: replyMessageID,
		},
	}
	if _, err := b.SendMediaGroupWithContext(ctx, chatID, trackMedia, sendOpts); nil != err {
		return fmt.Errorf("failed to send media album: %w", err)
	}

	return nil
}

func upload(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	dir fs.DownloadsDir,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	link types.Link,
) error {
	switch link.Kind {
	case types.LinkKindTrack:
		return uploadTrack(ctx, logger, b, dir, conf, chatID, replyMessageID, link)
	case types.LinkKindAlbum:
		return uploadAlbum(ctx, logger, b, dir, conf, chatID, replyMessageID, link.ID)
	case types.LinkKindPlaylist:
		return uploadPlaylist(ctx, logger, b, dir, conf, chatID, replyMessageID, link.ID)
	case types.LinkKindMix:
		return uploadMix(ctx, logger, b, dir, conf, chatID, replyMessageID, link.ID)
	case types.LinkKindArtist:
		return fmt.Errorf("unsupported link kind: %s", link.Kind)
	case types.LinkKindVideo:
		return fmt.Errorf("unsupported link kind: %s", link.Kind)
	default:
		panic(fmt.Sprintf("unsupported link kind: %s", link.Kind))
	}
}

func uploadTrack(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	dir fs.DownloadsDir,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	link types.Link,
) (err error) {
	track := dir.Track(link.ID)
	trackInfo, err := track.InfoFile.Read()
	if nil != err {
		logger.Error().Err(err).Msg("failed to read track info file")
		return fmt.Errorf("failed to read track info file: %v", err)
	}

	coverFile, err := os.Open(track.Cover.Path)
	if nil != err {
		logger.Error().Err(err).Msg("failed to open track cover file")
		return fmt.Errorf("failed to open cover file: %v", err)
	}
	defer func() {
		if closeErr := coverFile.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("failed to close track cover file")
			err = errors.Join(err, fmt.Errorf("failed to close track cover file: %v", closeErr))
		}
	}()

	trackFile, err := os.Open(track.Path)
	if nil != err {
		logger.Error().Err(err).Msg("failed to open track file")
		return fmt.Errorf("failed to open track file: %v", err)
	}
	defer func() {
		if closeErr := trackFile.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("failed to close track file")
			err = errors.Join(err, fmt.Errorf("failed to close track file: %v", closeErr))
		}
	}()

	trackMedia := gotgbot.InputFileByReader(trackInfo.UploadFilename(), trackFile)
	sendOpts := &gotgbot.SendAudioOpts{ //nolint:exhaustruct
		ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
			MessageId: replyMessageID,
		},
		Thumbnail: gotgbot.InputFileByReader("cover.jpg", coverFile),
		Duration:  int64(trackInfo.Duration),
		Performer: types.JoinArtists(trackInfo.Artists),
		Title:     trackInfo.UploadTitle(),
		Caption:   strings.Join([]string{trackInfo.Caption, "", conf.Signature}, "\n"),
		ParseMode: gotgbot.ParseModeMarkdown,
	}
	if _, err := b.SendAudioWithContext(ctx, chatID, trackMedia, sendOpts); nil != err {
		logger.Error().Err(err).Msg("failed to send audio")
		return fmt.Errorf("failed to send audio: %w", err)
	}

	return nil
}
