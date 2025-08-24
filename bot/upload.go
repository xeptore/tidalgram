package bot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

// func uploadAlbum(ctx context.Context, dir fs.DownloadDir, id string) error {
// 	albumFs := dir.Album(id)

// 	info, err := albumFs.InfoFile.Read()
// 	if nil != err {
// 		return fmt.Errorf("failed to read album info file: %v", err)
// 	}

// 	for volIdx, trackIDs := range info.VolumeTrackIDs {
// 		var (
// 			volNum     = volIdx + 1
// 			batchSize  = mathutil.OptimalAlbumSize(len(trackIDs))
// 			numBatches = mathutil.DivCeil(len(trackIDs), batchSize)
// 			batches    = iterutil.WithIndex(slices.Chunk(trackIDs, batchSize))
// 		)
// 		for i, trackIDs := range batches {
// 			caption := []styling.StyledTextOption{
// 				// styling.Plain(info.Caption),
// 				// styling.Plain("\n"),
// 				// styling.Italic(fmt.Sprintf("Part: %d/%d", i+1, numBatches)),
// 			}

// 			items := make([]TrackUploadInfo, len(trackIDs))
// 			for i, trackID := range trackIDs {
// 				trackFs := albumFs.Track(volNum, trackID)
// 				track, err := trackFs.InfoFile.Read()
// 				if nil != err {
// 					return err
// 				}
// 				info := TrackUploadInfo{
// 					FilePath:   trackFs.Path,
// 					ArtistName: types.JoinArtists(track.Artists),
// 					Title:      track.Title,
// 					Version:    track.Version,
// 					Duration:   track.Duration,
// 					Format:     track.Format,
// 					CoverID:    track.CoverID,
// 					CoverPath:  albumFs.Cover.Path,
// 				}
// 				items[i] = info
// 			}

// 			if err := uploadTracksBatch(ctx, items, caption); nil != err {
// 				return fmt.Errorf("failed to upload album tracks batch: %v", err)
// 			}
// 		}
// 	}
// 	return nil
// }

// func uploadPlaylist(ctx context.Context, dir fs.DownloadDir, id string) error {
// 	playlistFs := dir.Playlist(id)

// 	info, err := playlistFs.InfoFile.Read()
// 	if nil != err {
// 		return err
// 	}

// 	var (
// 		batchSize  = mathutil.OptimalAlbumSize(len(info.TrackIDs))
// 		batches    = iterutil.WithIndex(slices.Chunk(info.TrackIDs, batchSize))
// 		numBatches = mathutil.DivCeil(len(info.TrackIDs), batchSize)
// 	)
// 	for i, trackIDs := range batches {
// 		caption := []styling.StyledTextOption{
// 			// styling.Plain(info.Caption),
// 			// styling.Plain("\n"),
// 			// styling.Italic(fmt.Sprintf("Part: %d/%d", i+1, numBatches)),
// 		}

// 		items := make([]TrackUploadInfo, len(trackIDs))
// 		for i, trackID := range trackIDs {
// 			trackFs := playlistFs.Track(trackID)
// 			track, err := trackFs.InfoFile.Read()
// 			if nil != err {
// 				return err
// 			}
// 			info := TrackUploadInfo{
// 				FilePath:   trackFs.Path,
// 				ArtistName: types.JoinArtists(track.Artists),
// 				Title:      track.Title,
// 				Version:    track.Version,
// 				Duration:   track.Duration,
// 				Format:     track.Format,
// 				CoverID:    track.CoverID,
// 				CoverPath:  trackFs.Cover.Path,
// 			}
// 			items[i] = info
// 		}

// 		if err := uploadTracksBatch(ctx, items, caption); nil != err {
// 			return fmt.Errorf("failed to upload playlist tracks batch: %v", err)
// 		}
// 	}
// 	return nil
// }

// func uploadMix(ctx context.Context, dir fs.DownloadDir, id string) error {
// 	mixFs := dir.Mix(id)

// 	info, err := mixFs.InfoFile.Read()
// 	if nil != err {
// 		return err
// 	}

// 	var (
// 		batchSize  = mathutil.OptimalAlbumSize(len(info.TrackIDs))
// 		batches    = iterutil.WithIndex(slices.Chunk(info.TrackIDs, batchSize))
// 		numBatches = mathutil.DivCeil(len(info.TrackIDs), batchSize)
// 	)
// 	for i, trackIDs := range batches {
// 		caption := []styling.StyledTextOption{
// 			// styling.Plain(info.Caption),
// 			// styling.Plain("\n"),
// 			// styling.Italic(fmt.Sprintf("Part: %d/%d", i+1, numBatches)),
// 		}

// 		items := make([]TrackUploadInfo, len(trackIDs))
// 		for i, trackID := range trackIDs {
// 			trackFs := mixFs.Track(trackID)
// 			track, err := trackFs.InfoFile.Read()
// 			if nil != err {
// 				return err
// 			}
// 			info := TrackUploadInfo{
// 				FilePath:   trackFs.Path,
// 				ArtistName: types.JoinArtists(track.Artists),
// 				Title:      track.Title,
// 				Version:    track.Version,
// 				Duration:   track.Duration,
// 				Format:     track.Format,
// 				CoverID:    track.CoverID,
// 				CoverPath:  trackFs.Cover.Path,
// 			}
// 			items[i] = info
// 		}

// 		if err := uploadTracksBatch(ctx, items, caption); nil != err {
// 			return fmt.Errorf("failed to upload mix tracks batch: %v", err)
// 		}
// 	}
// 	return nil
// }

// func uploadTracksBatch(ctx context.Context, batch []TrackUploadInfo, caption []styling.StyledTextOption) (err error) {
// 	album := make([]message.MultiMediaOption, len(batch))

// 	wg, wgCtx := errgroup.WithContext(ctx)
// 	wg.SetLimit(ratelimit.BatchUploadConcurrency)

// 	for i, item := range batch {
// 		wg.Go(func() error {
// 			builder := newTrackUploadBuilder(&w.cache.UploadedCovers)
// 			if i == len(batch)-1 { // last track in this batch
// 				caption := append(caption, styling.Plain("\n"), html.String(nil, w.config.Signature))
// 				builder.WithCaption(caption)
// 			}
// 			document, err := builder.uploadTrack(wgCtx, w.logger, w.uploader, item)
// 			if nil != err {
// 				return fmt.Errorf("failed to upload track: %v", err)
// 			}
// 			album[i] = document
// 			return nil
// 		})
// 	}

// 	if err := wg.Wait(); nil != err {
// 		return fmt.Errorf("failed to upload tracks batch: %v", err)
// 	}

// 	var rest []message.MultiMediaOption
// 	if len(album) > 1 {
// 		rest = album[1:]
// 	}

// 	sendOpts := &gotgbot.SendMediaGroupOpts{
// 		ReplyParameters: &gotgbot.ReplyParameters{
// 			MessageId: replyToMessageID,
// 		},
// 	}

// 	var sentMessages []gotgbot.Message
// 	if msgs, err := b.SendMediaGroup(chatID, album, sendOpts); nil != err {
// 		return fmt.Errorf("failed to send media album: %v", err)
// 	} else if msgs[0].Chat.Type != gotgbot.ChatTypePrivate {
// 		if len(chunks) > 1 {
// 			time.Sleep(3 * time.Second) // avoid floodwait?
// 		}
// 	}
// 	return nil
// }

func uploadTrack(
	ctx context.Context,
	logger zerolog.Logger,
	b *gotgbot.Bot,
	dir fs.DownloadsDir,
	conf *config.Bot,
	chatID int64,
	replyMessageID int64,
	id string,
) (err error) {
	logger = logger.With().Str("track_id", id).Logger()

	track := dir.Track(id)
	info, err := track.InfoFile.Read()
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

	title := info.Title
	if nil != info.Version {
		title += " (" + *info.Version + ")"
	}

	trackFileName, err := getTrackFilename(logger, dir, id)
	if nil != err {
		return fmt.Errorf("failed to get track file name: %v", err)
	}

	coverFileName := trackFileName[:len(trackFileName)-len(filepath.Ext(trackFileName))] + ".jpg" // FIXME

	trackMedia := gotgbot.InputFileByReader(trackFileName, trackFile)
	sendOpts := &gotgbot.SendAudioOpts{ //nolint:exhaustruct
		ReplyParameters: &gotgbot.ReplyParameters{ //nolint:exhaustruct
			MessageId: replyMessageID,
		},
		Thumbnail: gotgbot.InputFileByReader(coverFileName, coverFile),
		Duration:  int64(info.Duration),
		Performer: types.JoinArtists(info.Artists),
		Title:     title,
		Caption:   strings.Join([]string{info.Caption, "", conf.Signature}, "\n"),
		ParseMode: gotgbot.ParseModeMarkdown,
	}
	if _, err := b.SendAudioWithContext(ctx, chatID, trackMedia, sendOpts); nil != err {
		logger.Error().Err(err).Msg("failed to send audio")
		return fmt.Errorf("failed to send audio: %v", err)
	}

	return nil
}

// FIXME.
func getTrackFilename(logger zerolog.Logger, dir fs.DownloadsDir, id string) (string, error) {
	track := dir.Track(id)
	info, err := track.InfoFile.Read()
	if nil != err {
		logger.Error().Err(err).Msg("failed to read track info file")
		return "", fmt.Errorf("failed to read track info file: %v", err)
	}

	artistName := types.JoinArtists(info.Artists)
	if nil != info.Version {
		return fmt.Sprintf("%s - %s (%s).%s", artistName, info.Title, *info.Version, info.Ext), nil
	}

	return fmt.Sprintf("%s - %s.%s", artistName, info.Title, info.Ext), nil
}
