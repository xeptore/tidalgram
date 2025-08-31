package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/contrib/bg"
	"github.com/gotd/td/constant"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/tclient"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/mathutil"
	"github.com/xeptore/tidalgram/telegram/progress"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

const MaxPartSize = constant.UploadMaxPartSize

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrPeerNotFound = errors.New("peer not found")
)

type Uploader struct {
	client *tg.Client
	stop   bg.StopFunc
	conf   config.Telegram
	peer   tg.InputPeerClass
	logger zerolog.Logger
}

func NewUploader(ctx context.Context, logger zerolog.Logger, conf config.Telegram) (*Uploader, error) {
	storage, err := NewStorage(conf.Storage.Path)
	if nil != err {
		return nil, fmt.Errorf("create storage: %v", err)
	}

	const maxRecoveryElapsedTime = 5 * time.Minute
	opts, err := newClientOptions(ctx, logger, storage, conf)
	if nil != err {
		return nil, fmt.Errorf("get client options: %w", err)
	}

	waiter := newWaiterMiddleware(logger)
	opts.Middlewares = []telegram.Middleware{
		waiter,
		newRateLimitMiddleware(),
	}

	client := telegram.NewClient(conf.AppID, conf.AppHash, *opts)

	stop, err := connect(ctx, logger, client, waiter)
	if nil != err {
		return nil, fmt.Errorf("connect to telegram: %w", err)
	}

	if status, err := client.Auth().Status(ctx); nil != err {
		return nil, fmt.Errorf("get auth status: %w", err)
	} else if !status.Authorized {
		return nil, ErrUnauthorized
	}

	user, err := client.Self(ctx)
	if nil != err {
		return nil, fmt.Errorf("get self: %w", err)
	}
	logger.Info().Int64("id", user.ID).Msg("Got self")

	pool := dcpool.NewPool(
		client,
		int64(conf.Upload.PoolSize),
		tclient.NewDefaultMiddlewares(ctx, maxRecoveryElapsedTime)...,
	)
	tgClient := pool.Default(ctx)

	var (
		peer      tg.InputPeerClass
		dialogKey dialogs.DialogKey
	)

	err = query.
		GetDialogs(tgClient).
		ForEach(ctx, func(ctx context.Context, elem dialogs.Elem) error {
			if err := dialogKey.FromInputPeer(elem.Peer); nil != err {
				return fmt.Errorf("get dialog key: %v", err)
			}

			switch dialogKey.Kind {
			case dialogs.User:
				if dialogKey.ID == conf.Upload.Peer.ID && conf.Upload.Peer.Kind == "user" {
					peer = elem.Peer
					return os.ErrExist
				}
			case dialogs.Chat:
				if dialogKey.ID == conf.Upload.Peer.ID && conf.Upload.Peer.Kind == "chat" {
					peer = elem.Peer
					return os.ErrExist
				}
			case dialogs.Channel:
				if dialogKey.ID == conf.Upload.Peer.ID && conf.Upload.Peer.Kind == "channel" {
					peer = elem.Peer
					return os.ErrExist
				}
			default:
				panic(fmt.Sprintf("invalid peer kind: %d", dialogKey.Kind))
			}

			return nil
		})
	if nil != err {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("get dialogs: %w", err)
		}
	}
	if peer == nil {
		return nil, ErrPeerNotFound
	}

	_, err = message.
		NewSender(tgClient).
		To(peer).
		Clear().
		Background().
		Silent().
		Text(ctx, "Hey! I'm here to upload your Tidal links.")
	if nil != err {
		return nil, fmt.Errorf("send message to peer: %w", err)
	}

	return &Uploader{
		client: tgClient,
		stop:   stop,
		conf:   conf,
		peer:   peer,
		logger: logger,
	}, nil
}

func (u *Uploader) Close() error {
	u.logger.Debug().Msg("Closing telegram uploader")
	if err := u.stop(); nil != err {
		return fmt.Errorf("stop background client: %v", err)
	}
	u.logger.Debug().Msg("Telegram uploader closed")

	return nil
}

func (u *Uploader) Upload(ctx context.Context, logger zerolog.Logger, dir fs.DownloadsDir, link types.Link) error {
	switch link.Kind {
	case types.LinkKindTrack:
		return u.uploadTrack(ctx, logger, dir, link.ID)
	case types.LinkKindAlbum:
		return u.uploadAlbum(ctx, logger, dir, link.ID)
	case types.LinkKindPlaylist:
		return u.uploadPlaylist(ctx, logger, dir, link.ID)
	case types.LinkKindMix:
		return u.uploadMix(ctx, logger, dir, link.ID)
	case types.LinkKindVideo:
		return errors.New("artist links are not supported")
	case types.LinkKindArtist:
		return errors.New("artist links are not supported")
	default:
		panic(fmt.Sprintf("unknown link kind: %s", link.Kind))
	}
}

func (u *Uploader) newUploader() *uploader.Uploader {
	return uploader.
		NewUploader(u.client).
		WithPartSize(MaxPartSize).
		WithThreads(u.conf.Upload.Threads)
}

func (u *Uploader) uploadAlbum(
	ctx context.Context,
	logger zerolog.Logger,
	dir fs.DownloadsDir,
	id string,
) (err error) {
	albumFs := dir.Album(id)
	info, err := albumFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("read playlist info file: %v", err)
	}

	coverInputFile, err := u.newUploader().FromPath(ctx, albumFs.Cover.Path)
	if nil != err {
		return fmt.Errorf("upload album track cover file: %w", err)
	}

	for volIdx, trackIDs := range info.VolumeTrackIDs {
		var (
			volNum     = volIdx + 1
			batchSize  = mathutil.OptimalAlbumSize(len(trackIDs))
			numBatches = mathutil.DivCeil(len(trackIDs), batchSize)
			batches    = slices.Collect(slices.Chunk(trackIDs, batchSize))
		)
		for partIdx, trackIDs := range batches {
			const notCollapsed = false
			partCaption := []message.StyledTextOption{
				styling.Blockquote(info.Caption, notCollapsed),
				styling.Plain("\n"),
				styling.Plain("\n"),
				styling.Italic(fmt.Sprintf("Volume: %d", volNum)),
				styling.Plain("\n"),
				styling.Italic(fmt.Sprintf("Part: %d/%d", partIdx+1, numBatches)),
			}

			tracker := progress.NewAlbumTracker(len(trackIDs))
			for i, trackID := range trackIDs {
				logger := logger.With().Int("index", i).Str("track_id", trackID).Logger()

				track := albumFs.Track(volNum, trackID)

				trackStat, err := os.Lstat(track.Path)
				if nil != err {
					logger.Error().Err(err).Msg("Failed to stat album track file")
					return fmt.Errorf("stat album track file: %v", err)
				}
				if !trackStat.Mode().IsRegular() {
					return fmt.Errorf("album track file %q is not a regular file", track.Path)
				}
				if trackStat.Size() == 0 {
					return errors.New("album track file is empty")
				}

				trackProgress := &progress.Track{Size: trackStat.Size()}

				// Cover is the same for all tracks in the album.
				// And, it is already uploaded.
				coverProgress := &progress.Cover{Size: 0}

				tracker.Set(i, progress.NewFileTracker(coverProgress, trackProgress))
			}

			wg, wgctx := errgroup.WithContext(ctx)
			wg.SetLimit(u.conf.Upload.Limit)

			typingWait := make(chan struct{})
			go u.keepTyping(ctx, tracker, typingWait, logger)

			album := make([]message.MultiMediaOption, len(trackIDs))
			for idx, trackID := range trackIDs {
				wg.Go(func() error {
					select {
					case <-wgctx.Done():
						return nil
					default:
					}

					logger := logger.With().Int("index", idx).Str("track_id", trackID).Logger()

					track := albumFs.Track(volNum, trackID)

					// Cover is the same for all tracks in the album.
					// And, it is already uploaded.
					trackProgress, _ := tracker.At(idx)

					trackInputFile, err := u.newUploader().WithProgress(trackProgress).FromPath(wgctx, track.Path)
					if nil != err {
						logger.Error().Err(err).Msg("Failed to upload album track file")
						return fmt.Errorf("upload album track file: %w", err)
					}

					mime, err := mimetype.DetectFile(track.Path)
					if nil != err {
						logger.Error().Err(err).Msg("Failed to detect album track mime")
						return fmt.Errorf("detect album track mime: %v", err)
					}

					var caption []message.StyledTextOption
					if idx == len(trackIDs)-1 {
						caption = append(caption, partCaption...)
						if sig := u.conf.Upload.Signature; len(sig) > 0 {
							caption = append(caption, html.String(nil, sig))
						}
					}

					trackInfo, err := track.InfoFile.Read()
					if nil != err {
						logger.Error().Err(err).Msg("Failed to read album track info file")
						return fmt.Errorf("read album track info file: %v", err)
					}

					doc := message.
						UploadedDocument(trackInputFile, caption...).
						MIME(mime.String()).
						Attributes(
							&tg.DocumentAttributeFilename{
								FileName: trackInfo.UploadFilename(),
							},
							//nolint:exhaustruct
							&tg.DocumentAttributeAudio{
								Title:     trackInfo.Title,
								Performer: types.JoinArtists(trackInfo.Artists),
								Duration:  trackInfo.Duration,
							}).
						Thumb(coverInputFile).
						Audio().
						DurationSeconds(trackInfo.Duration).
						Performer(types.JoinArtists(trackInfo.Artists)).
						Title(trackInfo.Title)

					album[idx] = doc

					return nil
				})
			}

			if err := wg.Wait(); nil != err {
				return fmt.Errorf("upload album: %w", err)
			}

			var rest []message.MultiMediaOption
			if len(album) > 1 {
				rest = album[1:]
			}

			_, err = message.
				NewSender(u.client).
				WithUploader(u.newUploader()).
				To(u.peer).
				Clear().
				Background().
				Silent().
				Album(ctx, album[0], rest...)
			if nil != err {
				return fmt.Errorf("send mix: %w", err)
			}

			select {
			case <-typingWait:
			case <-ctx.Done():
				return fmt.Errorf("wait for typing: %w", ctx.Err())
			}
		}
	}

	return nil
}

func (u *Uploader) uploadMix(
	ctx context.Context,
	logger zerolog.Logger,
	dir fs.DownloadsDir,
	id string,
) (err error) {
	mixFs := dir.Mix(id)
	info, err := mixFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("read playlist info file: %v", err)
	}

	var (
		batchSize  = mathutil.OptimalAlbumSize(len(info.TrackIDs))
		batches    = slices.Collect(slices.Chunk(info.TrackIDs, batchSize))
		numBatches = mathutil.DivCeil(len(info.TrackIDs), batchSize)
	)
	for partIdx, trackIDs := range batches {
		const notCollapsed = false
		partCaption := []styling.StyledTextOption{
			styling.Blockquote(info.Caption, notCollapsed),
			styling.Plain("\n"),
			styling.Plain("\n"),
			styling.Italic(fmt.Sprintf("Part: %d/%d", partIdx+1, numBatches)),
		}

		tracker := progress.NewAlbumTracker(len(trackIDs))
		for i, trackID := range trackIDs {
			logger := logger.With().Int("index", i).Str("track_id", trackID).Logger()

			track := mixFs.Track(trackID)

			trackStat, err := os.Lstat(track.Path)
			if nil != err {
				logger.Error().Err(err).Msg("Failed to stat mix track file")
				return fmt.Errorf("stat mix track file: %v", err)
			}
			if !trackStat.Mode().IsRegular() {
				return fmt.Errorf("mix track file %q is not a regular file", track.Path)
			}
			if trackStat.Size() == 0 {
				return errors.New("mix track file is empty")
			}

			trackProgress := &progress.Track{Size: trackStat.Size()}

			coverStat, err := os.Lstat(track.Cover.Path)
			if nil != err {
				logger.Error().Err(err).Msg("Failed to stat mix track cover file")
				return fmt.Errorf("stat mix track cover file: %v", err)
			}
			if !coverStat.Mode().IsRegular() {
				return fmt.Errorf("mix track cover file %q is not a regular file", track.Cover.Path)
			}
			if coverStat.Size() == 0 {
				return errors.New("mix track cover file is empty")
			}

			coverProgress := &progress.Cover{Size: coverStat.Size()}

			tracker.Set(i, progress.NewFileTracker(coverProgress, trackProgress))
		}

		wg, wgctx := errgroup.WithContext(ctx)
		wg.SetLimit(u.conf.Upload.Limit)

		typingWait := make(chan struct{})
		go u.keepTyping(ctx, tracker, typingWait, logger)

		album := make([]message.MultiMediaOption, len(trackIDs))
		for i, trackID := range trackIDs {
			wg.Go(func() (err error) {
				select {
				case <-wgctx.Done():
					return nil
				default:
				}

				logger := logger.With().Int("index", i).Str("track_id", trackID).Logger()

				track := mixFs.Track(trackID)

				trackProgress, coverProgress := tracker.At(i)

				trackInputFile, err := u.newUploader().WithProgress(trackProgress).FromPath(wgctx, track.Path)
				if nil != err {
					return fmt.Errorf("upload mix track file: %w", err)
				}

				coverInputFile, err := u.newUploader().WithProgress(coverProgress).FromPath(wgctx, track.Cover.Path)
				if nil != err {
					return fmt.Errorf("upload mix track cover file: %w", err)
				}

				mime, err := mimetype.DetectFile(track.Path)
				if nil != err {
					logger.Error().Err(err).Msg("Failed to detect mix mime")
					return fmt.Errorf("detect mix mime: %v", err)
				}

				var caption []message.StyledTextOption
				if i == len(trackIDs)-1 {
					caption = append(caption, partCaption...)
					if sig := u.conf.Upload.Signature; len(sig) > 0 {
						caption = append(caption, html.String(nil, sig))
					}
				}

				trackInfo, err := track.InfoFile.Read()
				if nil != err {
					logger.Error().Err(err).Msg("Failed to read mix track info file")
					return fmt.Errorf("read mix track info file: %v", err)
				}

				doc := message.
					UploadedDocument(trackInputFile, caption...).
					MIME(mime.String()).
					Attributes(
						&tg.DocumentAttributeFilename{
							FileName: trackInfo.UploadFilename(),
						},
						//nolint:exhaustruct
						&tg.DocumentAttributeAudio{
							Title:     trackInfo.Title,
							Performer: types.JoinArtists(trackInfo.Artists),
							Duration:  trackInfo.Duration,
						}).
					Thumb(coverInputFile).
					Audio().
					DurationSeconds(trackInfo.Duration).
					Performer(types.JoinArtists(trackInfo.Artists)).
					Title(trackInfo.Title)

				album[i] = doc

				return nil
			})
		}

		if err := wg.Wait(); nil != err {
			return fmt.Errorf("wait for upload mix tracks: %w", err)
		}

		var rest []message.MultiMediaOption
		if len(album) > 1 {
			rest = album[1:]
		}

		_, err = message.
			NewSender(u.client).
			WithUploader(u.newUploader()).
			To(u.peer).
			Clear().
			Background().
			Silent().
			Album(ctx, album[0], rest...)
		if nil != err {
			return fmt.Errorf("send mix: %w", err)
		}

		select {
		case <-typingWait:
		case <-ctx.Done():
			return fmt.Errorf("wait for typing: %w", ctx.Err())
		}
	}

	return nil
}

func (u *Uploader) uploadPlaylist(
	ctx context.Context,
	logger zerolog.Logger,
	dir fs.DownloadsDir,
	id string,
) (err error) {
	playlistFs := dir.Playlist(id)
	info, err := playlistFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("read playlist info file: %v", err)
	}

	var (
		batchSize  = mathutil.OptimalAlbumSize(len(info.TrackIDs))
		batches    = slices.Collect(slices.Chunk(info.TrackIDs, batchSize))
		numBatches = mathutil.DivCeil(len(info.TrackIDs), batchSize)
	)
	for partIdx, trackIDs := range batches {
		const notCollapsed = false
		partCaption := []styling.StyledTextOption{
			styling.Blockquote(info.Caption, notCollapsed),
			styling.Plain("\n"),
			styling.Plain("\n"),
			styling.Italic(fmt.Sprintf("Part: %d/%d", partIdx+1, numBatches)),
		}

		tracker := progress.NewAlbumTracker(len(trackIDs))
		for i, trackID := range trackIDs {
			logger := logger.With().Int("index", i).Str("track_id", trackID).Logger()

			track := playlistFs.Track(trackID)

			trackStat, err := os.Lstat(track.Path)
			if nil != err {
				logger.Error().Err(err).Msg("Failed to stat playlist track file")
				return fmt.Errorf("stat playlist track file: %v", err)
			}
			if !trackStat.Mode().IsRegular() {
				return fmt.Errorf("playlist track file %q is not a regular file", track.Path)
			}
			if trackStat.Size() == 0 {
				return errors.New("playlist track file is empty")
			}

			trackProgress := &progress.Track{Size: trackStat.Size()}

			coverStat, err := os.Lstat(track.Cover.Path)
			if nil != err {
				logger.Error().Err(err).Msg("Failed to stat playlist track cover file")
				return fmt.Errorf("stat playlist track cover file: %v", err)
			}
			if !coverStat.Mode().IsRegular() {
				return fmt.Errorf("playlist track cover file %q is not a regular file", track.Cover.Path)
			}
			if coverStat.Size() == 0 {
				return errors.New("playlist track cover file is empty")
			}

			coverProgress := &progress.Cover{Size: coverStat.Size()}

			tracker.Set(i, progress.NewFileTracker(coverProgress, trackProgress))
		}

		wg, wgctx := errgroup.WithContext(ctx)
		wg.SetLimit(u.conf.Upload.Limit)

		typingWait := make(chan struct{})
		go u.keepTyping(ctx, tracker, typingWait, logger)

		album := make([]message.MultiMediaOption, len(trackIDs))
		for idx, trackID := range trackIDs {
			wg.Go(func() error {
				select {
				case <-wgctx.Done():
					return nil
				default:
				}

				logger := logger.With().Int("index", idx).Str("track_id", trackID).Logger()

				track := playlistFs.Track(trackID)

				trackProgress, coverProgress := tracker.At(idx)

				trackInputFile, err := u.newUploader().WithProgress(trackProgress).FromPath(wgctx, track.Path)
				if nil != err {
					return fmt.Errorf("upload playlist track file: %w", err)
				}

				coverInputFile, err := u.newUploader().WithProgress(coverProgress).FromPath(wgctx, track.Cover.Path)
				if nil != err {
					return fmt.Errorf("upload playlist track cover file: %w", err)
				}

				trackInfo, err := track.InfoFile.Read()
				if nil != err {
					logger.Error().Err(err).Msg("Failed to read playlist track info file")
					return fmt.Errorf("read track info file: %v", err)
				}

				mime, err := mimetype.DetectFile(track.Path)
				if nil != err {
					logger.Error().Err(err).Msg("Failed to detect playlist mime")
					return fmt.Errorf("detect playlist mime: %v", err)
				}

				var caption []message.StyledTextOption
				if idx == len(trackIDs)-1 {
					caption = append(caption, partCaption...)
					if sig := u.conf.Upload.Signature; len(sig) > 0 {
						caption = append(caption, html.String(nil, sig))
					}
				}

				doc := message.
					UploadedDocument(trackInputFile, caption...).
					MIME(mime.String()).
					Attributes(
						&tg.DocumentAttributeFilename{
							FileName: trackInfo.UploadFilename(),
						},
						//nolint:exhaustruct
						&tg.DocumentAttributeAudio{
							Title:     trackInfo.Title,
							Performer: types.JoinArtists(trackInfo.Artists),
							Duration:  trackInfo.Duration,
						}).
					Thumb(coverInputFile).
					Audio().
					DurationSeconds(trackInfo.Duration).
					Performer(types.JoinArtists(trackInfo.Artists)).
					Title(trackInfo.Title)

				album[idx] = doc

				return nil
			})
		}

		if err := wg.Wait(); nil != err {
			return fmt.Errorf("upload playlist: %w", err)
		}

		var rest []message.MultiMediaOption
		if len(album) > 1 {
			rest = album[1:]
		}

		_, err = message.
			NewSender(u.client).
			WithUploader(u.newUploader()).
			To(u.peer).
			Clear().
			Background().
			Silent().
			Album(ctx, album[0], rest...)
		if nil != err {
			return fmt.Errorf("send playlist: %w", err)
		}

		select {
		case <-typingWait:
		case <-ctx.Done():
			return fmt.Errorf("wait for typing: %w", ctx.Err())
		}
	}

	return nil
}

func (u *Uploader) uploadTrack(ctx context.Context, logger zerolog.Logger, dir fs.DownloadsDir, id string) error {
	track := dir.Track(id)
	trackInfo, err := track.InfoFile.Read()
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read track info file")
		return fmt.Errorf("read track info file: %v", err)
	}

	trackStat, err := os.Lstat(track.Path)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to stat track file")
		return fmt.Errorf("stat track file: %v", err)
	}
	if !trackStat.Mode().IsRegular() {
		return fmt.Errorf("track file %q is not a regular file", track.Path)
	}
	if trackStat.Size() == 0 {
		return errors.New("track file is empty")
	}
	trackProgress := &progress.Track{Size: trackStat.Size()}

	coverStat, err := os.Lstat(track.Cover.Path)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to stat track cover file")
		return fmt.Errorf("stat track cover file: %v", err)
	}
	if !coverStat.Mode().IsRegular() {
		return fmt.Errorf("track cover file %q is not a regular file", track.Cover.Path)
	}
	if coverStat.Size() == 0 {
		return errors.New("track cover file is empty")
	}
	coverProgress := &progress.Cover{Size: coverStat.Size()}

	tracker := progress.NewFileTracker(coverProgress, trackProgress)

	typingWait := make(chan struct{})
	go u.keepTyping(ctx, tracker, typingWait, logger)

	trackInputFile, err := u.newUploader().WithProgress(trackProgress).FromPath(ctx, track.Path)
	if nil != err {
		return fmt.Errorf("upload track file: %w", err)
	}

	coverInputFile, err := u.newUploader().WithProgress(coverProgress).FromPath(ctx, track.Cover.Path)
	if nil != err {
		return fmt.Errorf("upload track cover file: %w", err)
	}

	select {
	case <-typingWait:
	case <-ctx.Done():
		return fmt.Errorf("wait for typing: %w", ctx.Err())
	}

	mime, err := mimetype.DetectFile(track.Path)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to detect track mime")
		return fmt.Errorf("detect mime: %v", err)
	}

	const notCollapsed = false
	caption := []message.StyledTextOption{
		styling.Blockquote(trackInfo.Caption, notCollapsed),
	}
	if sig := u.conf.Upload.Signature; len(sig) > 0 {
		caption = append(caption, html.String(nil, sig))
	}

	doc := message.
		UploadedDocument(trackInputFile, caption...).
		MIME(mime.String()).
		Attributes(
			&tg.DocumentAttributeFilename{
				FileName: trackInfo.UploadFilename(),
			},
			//nolint:exhaustruct
			&tg.DocumentAttributeAudio{
				Title:     trackInfo.Title,
				Performer: types.JoinArtists(trackInfo.Artists),
				Duration:  trackInfo.Duration,
			}).
		Thumb(coverInputFile).
		Audio().
		DurationSeconds(trackInfo.Duration).
		Performer(types.JoinArtists(trackInfo.Artists)).
		Title(trackInfo.Title)

	_, err = message.
		NewSender(u.client).
		WithUploader(u.newUploader()).
		To(u.peer).
		Clear().
		Background().
		Silent().
		Media(ctx, doc)
	if nil != err {
		return fmt.Errorf("send message: %w", err)
	}

	return nil
}

func (u *Uploader) cancelTyping(ctx context.Context) {
	req := &tg.MessagesSetTypingRequest{ //nolint:exhaustruct
		Peer:   u.peer,
		Action: &tg.SendMessageCancelAction{},
	}
	if ok, err := u.client.MessagesSetTyping(ctx, req); nil != err {
		u.logger.Error().Err(err).Msg("Failed to cancel typing action")
	} else if !ok {
		u.logger.Error().Msg("Failed to cancel typing action: not ok")
	}
}

func (u *Uploader) sendTyping(ctx context.Context, logger zerolog.Logger, tracker progress.Tracker) error {
	percent := tracker.Percent()
	logger.Debug().Int("percent", percent).Msg("Sending typing action")

	if percent == 100 {
		return os.ErrProcessDone
	}

	req := &tg.MessagesSetTypingRequest{ //nolint:exhaustruct
		Peer: u.peer,
		Action: &tg.SendMessageUploadDocumentAction{
			Progress: percent,
		},
	}
	if ok, err := u.client.MessagesSetTyping(ctx, req); nil != err {
		return fmt.Errorf("send typing action: %w", err)
	} else if !ok {
		return errors.New("send typing action: not ok")
	}

	return nil
}

func (u *Uploader) keepTyping(
	ctx context.Context,
	tracker progress.Tracker,
	wait chan<- struct{},
	logger zerolog.Logger,
) {
	defer close(wait)

	ticker := time.NewTicker(1221 * time.Millisecond)
	defer ticker.Stop()
	defer u.cancelTyping(ctx)

	if err := u.sendTyping(ctx, logger, tracker); nil != err {
		if !errors.Is(err, os.ErrProcessDone) {
			logger.Error().Err(err).Msg("Failed to send typing action")
			return
		}

		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := u.sendTyping(ctx, logger, tracker); nil != err {
				if !errors.Is(err, os.ErrProcessDone) {
					logger.Error().Err(err).Msg("Failed to send typing action")
					return
				}

				return
			}
		}
	}
}
