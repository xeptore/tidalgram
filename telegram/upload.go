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
	engine *uploader.Uploader
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
	engine := uploader.
		NewUploader(tgClient).
		WithPartSize(MaxPartSize).
		WithThreads(conf.Upload.Threads)

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
		engine: engine,
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

	coverInputFile, err := u.engine.FromPath(ctx, albumFs.Cover.Path)
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

			wg, wgctx := errgroup.WithContext(ctx)
			wg.SetLimit(u.conf.Upload.Limit)

			album := make([]message.MultiMediaOption, len(trackIDs))
			for idx, trackID := range trackIDs {
				wg.Go(func() error {
					select {
					case <-wgctx.Done():
						return nil
					default:
					}

					logger := logger.With().Int("index", idx).Logger()

					logger = logger.With().Str("track_id", trackID).Logger()
					track := albumFs.Track(volNum, trackID)
					trackInfo, err := track.InfoFile.Read()
					if nil != err {
						logger.Error().Err(err).Msg("read album track info file")
						return fmt.Errorf("read album track info file: %v", err)
					}

					trackInputFile, err := u.engine.FromPath(wgctx, track.Path)
					if nil != err {
						logger.Error().Err(err).Msg("upload album track file")
						return fmt.Errorf("upload album track file: %w", err)
					}

					mime, err := mimetype.DetectFile(track.Path)
					if nil != err {
						logger.Error().Err(err).Msg("detect album track mime")
						return fmt.Errorf("detect album track mime: %v", err)
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

					time.Sleep(u.conf.Upload.PauseDuration.Duration)

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
				WithUploader(u.engine).
				To(u.peer).
				Clear().
				Background().
				Silent().
				Album(ctx, album[0], rest...)
			if nil != err {
				return fmt.Errorf("send mix: %w", err)
			}
		}
	}

	time.Sleep(u.conf.Upload.PauseDuration.Duration)

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

		wg, wgctx := errgroup.WithContext(ctx)
		wg.SetLimit(len(trackIDs))

		for i, trackID := range trackIDs {
			wg.Go(func() (err error) {
				select {
				case <-wgctx.Done():
					return nil
				default:
				}

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

				return nil
			})
		}

		if err := wg.Wait(); nil != err {
			return fmt.Errorf("wait for stats of mix tracks: %w", err)
		}

		wg, wgctx = errgroup.WithContext(ctx)
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

				trackInputFile, err := u.engine.WithProgress(trackProgress).FromPath(wgctx, track.Path)
				if nil != err {
					return fmt.Errorf("upload mix track file: %w", err)
				}

				coverInputFile, err := u.engine.WithProgress(coverProgress).FromPath(wgctx, track.Cover.Path)
				if nil != err {
					return fmt.Errorf("upload mix track cover file: %w", err)
				}

				mime, err := mimetype.DetectFile(track.Path)
				if nil != err {
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
					logger.Error().Err(err).Msg("read mix track info file")
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

				time.Sleep(u.conf.Upload.PauseDuration.Duration)

				return nil
			})
		}

		if err := wg.Wait(); nil != err {
			return fmt.Errorf("wait for upload mix tracks: %w", err)
		}

		select {
		case <-typingWait:
		case <-ctx.Done():
			return ctx.Err()
		}

		var rest []message.MultiMediaOption
		if len(album) > 1 {
			rest = album[1:]
		}

		_, err = message.
			NewSender(u.client).
			WithUploader(u.engine).
			To(u.peer).
			Clear().
			Background().
			Silent().
			Album(ctx, album[0], rest...)
		if nil != err {
			return fmt.Errorf("send mix: %w", err)
		}
	}

	time.Sleep(u.conf.Upload.PauseDuration.Duration)

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

		wg, wgctx := errgroup.WithContext(ctx)
		wg.SetLimit(u.conf.Upload.Limit)

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
				trackInfo, err := track.InfoFile.Read()
				if nil != err {
					logger.Error().Err(err).Msg("read playlist track info file")
					return fmt.Errorf("read track info file: %v", err)
				}

				trackInputFile, err := u.engine.FromPath(wgctx, track.Path)
				if nil != err {
					return fmt.Errorf("upload playlist track file: %w", err)
				}

				coverInputFile, err := u.engine.FromPath(wgctx, track.Cover.Path)
				if nil != err {
					return fmt.Errorf("upload playlist track cover file: %w", err)
				}

				mime, err := mimetype.DetectFile(track.Path)
				if nil != err {
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

				time.Sleep(u.conf.Upload.PauseDuration.Duration)

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
			WithUploader(u.engine).
			To(u.peer).
			Clear().
			Background().
			Silent().
			Album(ctx, album[0], rest...)
		if nil != err {
			return fmt.Errorf("send playlist: %w", err)
		}
	}

	time.Sleep(u.conf.Upload.PauseDuration.Duration)

	return nil
}

func (u *Uploader) uploadTrack(ctx context.Context, logger zerolog.Logger, dir fs.DownloadsDir, id string) error {
	track := dir.Track(id)
	trackInfo, err := track.InfoFile.Read()
	if nil != err {
		logger.Error().Err(err).Msg("read track info file")
		return fmt.Errorf("read track info file: %v", err)
	}

	trackStat, err := os.Lstat(track.Path)
	if nil != err {
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

	trackInputFile, err := u.engine.WithProgress(trackProgress).FromPath(ctx, track.Path)
	if nil != err {
		return fmt.Errorf("upload track file: %w", err)
	}

	coverInputFile, err := u.engine.WithProgress(coverProgress).FromPath(ctx, track.Cover.Path)
	if nil != err {
		return fmt.Errorf("upload track cover file: %w", err)
	}

	select {
	case <-typingWait:
	case <-ctx.Done():
		return ctx.Err()
	}

	mime, err := mimetype.DetectFile(track.Path)
	if nil != err {
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
		WithUploader(u.engine).
		To(u.peer).
		Clear().
		Background().
		Silent().
		Media(ctx, doc)
	if nil != err {
		return fmt.Errorf("send message: %w", err)
	}

	time.Sleep(u.conf.Upload.PauseDuration.Duration)

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
