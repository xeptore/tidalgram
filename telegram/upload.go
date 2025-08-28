package telegram

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gotd/contrib/bg"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/tclient"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/mathutil"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

// MaxPartSize refer to https://core.telegram.org/api/files#uploading-files
const MaxPartSize = 512 * 1024

var ErrUnauthorized = errors.New("unauthorized")

type Uploader struct {
	client *tg.Client
	stop   bg.StopFunc
	conf   config.Telegram
	engine *uploader.Uploader
	peer   tg.InputPeerClass
	logger zerolog.Logger
}

func NewUploader(
	ctx context.Context,
	logger zerolog.Logger,
	conf config.Telegram,
	peerUserID int64,
) (*Uploader, error) {
	storage, err := NewStorage(conf.Storage.Path)
	if nil != err {
		return nil, fmt.Errorf("failed to create storage: %v", err)
	}

	const maxRecoveryElapsedTime = 5 * time.Minute
	opts, err := newClientOptions(ctx, logger, storage, conf)
	if nil != err {
		return nil, fmt.Errorf("failed to get client options: %v", err)
	}

	waiter := newWaiterMiddleware(logger)
	opts.Middlewares = []telegram.Middleware{
		waiter,
		newRateLimitMiddleware(),
	}

	client := telegram.NewClient(conf.AppID, conf.AppHash, *opts)

	stop, err := connect(ctx, logger, client, waiter)
	if nil != err {
		return nil, fmt.Errorf("failed to connect to telegram: %w", err)
	}

	if status, err := client.Auth().Status(ctx); nil != err {
		return nil, fmt.Errorf("failed to get auth status: %w", err)
	} else if !status.Authorized {
		return nil, ErrUnauthorized
	}

	user, err := client.Self(ctx)
	if nil != err {
		return nil, fmt.Errorf("failed to get self: %w", err)
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

	_, err = message.
		NewSender(tgClient).
		To(&tg.InputPeerUser{UserID: peerUserID}). //nolint:exhaustruct
		Clear().
		Background().
		Silent().
		Text(ctx, "Hey! I'm TidalGram uploader!")
	if nil != err {
		return nil, fmt.Errorf("failed to send message to peer: %w", err)
	}

	return &Uploader{
		client: tgClient,
		stop:   stop,
		conf:   conf,
		engine: engine,
		peer:   &tg.InputPeerUser{UserID: peerUserID}, //nolint:exhaustruct
		logger: logger,
	}, nil
}

func (c *Uploader) Close() error {
	c.logger.Debug().Msg("Closing telegram uploader")
	if err := c.stop(); nil != err {
		return fmt.Errorf("failed to stop background client: %v", err)
	}
	c.logger.Debug().Msg("Telegram uploader closed")

	return nil
}

func (c *Uploader) Upload(ctx context.Context, logger zerolog.Logger, dir fs.DownloadsDir, link types.Link) error {
	switch link.Kind {
	case types.LinkKindTrack:
		return c.uploadTrack(ctx, logger, dir, link.ID)
	case types.LinkKindAlbum:
		return c.uploadAlbum(ctx, logger, dir, link.ID)
	case types.LinkKindPlaylist:
		return c.uploadPlaylist(ctx, logger, dir, link.ID)
	case types.LinkKindMix:
		return c.uploadMix(ctx, logger, dir, link.ID)
	case types.LinkKindVideo:
		return errors.New("artist links are not supported")
	case types.LinkKindArtist:
		return errors.New("artist links are not supported")
	default:
		panic(fmt.Sprintf("unknown link kind: %s", link.Kind))
	}
}

func (c *Uploader) uploadAlbum(
	ctx context.Context,
	logger zerolog.Logger,
	dir fs.DownloadsDir,
	id string,
) (err error) {
	albumFs := dir.Album(id)
	info, err := albumFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("failed to read playlist info file: %v", err)
	}

	coverInputFile, err := c.engine.FromPath(ctx, albumFs.Cover.Path)
	if nil != err {
		return fmt.Errorf("failed to upload album track cover file: %w", err)
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
				styling.Plain("\n"),
				styling.Blockquote(info.Caption, notCollapsed),
				styling.Plain("\n"),
				styling.Plain("\n"),
				styling.Italic(fmt.Sprintf("Volume: %d", volNum)),
				styling.Plain("\n"),
				styling.Italic(fmt.Sprintf("Part: %d/%d", partIdx+1, numBatches)),
			}

			wg, wgctx := errgroup.WithContext(ctx)
			wg.SetLimit(c.conf.Upload.Limit)

			album := make([]message.MultiMediaOption, len(trackIDs))
			for idx, trackID := range trackIDs {
				logger := logger.With().Int("index", idx).Logger()

				wg.Go(func() error {
					logger = logger.With().Str("track_id", trackID).Logger()
					track := albumFs.Track(volNum, trackID)
					trackInfo, err := track.InfoFile.Read()
					if nil != err {
						logger.Error().Err(err).Msg("Failed to read album track info file")
						return fmt.Errorf("failed to read album track info file: %v", err)
					}

					trackInputFile, err := c.engine.FromPath(wgctx, track.Path)
					if nil != err {
						logger.Error().Err(err).Msg("Failed to upload album track file")
						return fmt.Errorf("failed to upload album track file: %w", err)
					}

					mime, err := mimetype.DetectFile(track.Path)
					if nil != err {
						logger.Error().Err(err).Msg("Failed to detect album track mime")
						return fmt.Errorf("failed to detect album track mime: %v", err)
					}

					var caption []message.StyledTextOption
					if idx == len(trackIDs)-1 {
						caption = append(caption, partCaption...)
						if sig := c.conf.Upload.Signature; len(sig) > 0 {
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

					time.Sleep(c.conf.Upload.PauseDuration.Duration)

					return nil
				})
			}

			if err := wg.Wait(); nil != err {
				return fmt.Errorf("failed to upload album: %w", err)
			}

			var rest []message.MultiMediaOption
			if len(album) > 1 {
				rest = album[1:]
			}

			_, err = message.
				NewSender(c.client).
				WithUploader(c.engine).
				To(c.peer).
				Clear().
				Album(ctx, album[0], rest...)
			if nil != err {
				return fmt.Errorf("failed to send mix: %w", err)
			}
		}
	}

	return nil
}

func (c *Uploader) uploadMix(
	ctx context.Context,
	logger zerolog.Logger,
	dir fs.DownloadsDir,
	id string,
) (err error) {
	mixFs := dir.Mix(id)
	info, err := mixFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("failed to read playlist info file: %v", err)
	}

	var (
		batchSize  = mathutil.OptimalAlbumSize(len(info.TrackIDs))
		batches    = slices.Collect(slices.Chunk(info.TrackIDs, batchSize))
		numBatches = mathutil.DivCeil(len(info.TrackIDs), batchSize)
	)
	for partIdx, trackIDs := range batches {
		const notCollapsed = false
		partCaption := []styling.StyledTextOption{
			styling.Plain("\n"),
			styling.Blockquote(info.Caption, notCollapsed),
			styling.Plain("\n"),
			styling.Plain("\n"),
			styling.Italic(fmt.Sprintf("Part: %d/%d", partIdx+1, numBatches)),
		}

		wg, wgctx := errgroup.WithContext(ctx)
		wg.SetLimit(c.conf.Upload.Limit)

		album := make([]message.MultiMediaOption, len(trackIDs))
		for idx, trackID := range trackIDs {
			logger := logger.With().Int("index", idx).Logger()

			wg.Go(func() error {
				logger = logger.With().Str("track_id", trackID).Logger()
				track := mixFs.Track(trackID)
				trackInfo, err := track.InfoFile.Read()
				if nil != err {
					logger.Error().Err(err).Msg("failed to read mix track info file")
					return fmt.Errorf("failed to read mix track info file: %v", err)
				}

				trackInputFile, err := c.engine.FromPath(wgctx, track.Path)
				if nil != err {
					return fmt.Errorf("failed to upload mix track file: %w", err)
				}

				coverInputFile, err := c.engine.FromPath(wgctx, track.Cover.Path)
				if nil != err {
					return fmt.Errorf("failed to upload mix track cover file: %w", err)
				}

				mime, err := mimetype.DetectFile(track.Path)
				if nil != err {
					return fmt.Errorf("failed to detect mix mime: %v", err)
				}

				var caption []message.StyledTextOption
				if idx == len(trackIDs)-1 {
					caption = append(caption, partCaption...)
					if sig := c.conf.Upload.Signature; len(sig) > 0 {
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

				time.Sleep(c.conf.Upload.PauseDuration.Duration)

				return nil
			})
		}

		if err := wg.Wait(); nil != err {
			return fmt.Errorf("failed to upload mix: %w", err)
		}

		var rest []message.MultiMediaOption
		if len(album) > 1 {
			rest = album[1:]
		}

		_, err = message.
			NewSender(c.client).
			WithUploader(c.engine).
			To(c.peer).
			Clear().
			Album(ctx, album[0], rest...)
		if nil != err {
			return fmt.Errorf("failed to send mix: %w", err)
		}
	}

	return nil
}

func (c *Uploader) uploadPlaylist(
	ctx context.Context,
	logger zerolog.Logger,
	dir fs.DownloadsDir,
	id string,
) (err error) {
	playlistFs := dir.Playlist(id)
	info, err := playlistFs.InfoFile.Read()
	if nil != err {
		return fmt.Errorf("failed to read playlist info file: %v", err)
	}

	var (
		batchSize  = mathutil.OptimalAlbumSize(len(info.TrackIDs))
		batches    = slices.Collect(slices.Chunk(info.TrackIDs, batchSize))
		numBatches = mathutil.DivCeil(len(info.TrackIDs), batchSize)
	)
	for partIdx, trackIDs := range batches {
		const notCollapsed = false
		partCaption := []styling.StyledTextOption{
			styling.Plain("\n"),
			styling.Blockquote(info.Caption, notCollapsed),
			styling.Plain("\n"),
			styling.Plain("\n"),
			styling.Italic(fmt.Sprintf("Part: %d/%d", partIdx+1, numBatches)),
		}

		wg, wgctx := errgroup.WithContext(ctx)
		wg.SetLimit(c.conf.Upload.Limit)

		album := make([]message.MultiMediaOption, len(trackIDs))
		for idx, trackID := range trackIDs {
			logger := logger.With().Int("index", idx).Logger()

			wg.Go(func() error {
				logger = logger.With().Str("track_id", trackID).Logger()
				track := playlistFs.Track(trackID)
				trackInfo, err := track.InfoFile.Read()
				if nil != err {
					logger.Error().Err(err).Msg("failed to read playlist track info file")
					return fmt.Errorf("failed to read track info file: %v", err)
				}

				trackInputFile, err := c.engine.FromPath(wgctx, track.Path)
				if nil != err {
					return fmt.Errorf("failed to upload playlist track file: %w", err)
				}

				coverInputFile, err := c.engine.FromPath(wgctx, track.Cover.Path)
				if nil != err {
					return fmt.Errorf("failed to upload playlist track cover file: %w", err)
				}

				mime, err := mimetype.DetectFile(track.Path)
				if nil != err {
					return fmt.Errorf("failed to detect playlist mime: %v", err)
				}

				var caption []message.StyledTextOption
				if idx == len(trackIDs)-1 {
					caption = append(caption, partCaption...)
					if sig := c.conf.Upload.Signature; len(sig) > 0 {
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

				time.Sleep(c.conf.Upload.PauseDuration.Duration)

				return nil
			})
		}

		if err := wg.Wait(); nil != err {
			return fmt.Errorf("failed to upload playlist: %w", err)
		}

		var rest []message.MultiMediaOption
		if len(album) > 1 {
			rest = album[1:]
		}

		_, err = message.
			NewSender(c.client).
			WithUploader(c.engine).
			To(c.peer).
			Clear().
			Album(ctx, album[0], rest...)
		if nil != err {
			return fmt.Errorf("failed to send playlist: %w", err)
		}
	}

	return nil
}

func (c *Uploader) uploadTrack(ctx context.Context, logger zerolog.Logger, dir fs.DownloadsDir, id string) error {
	track := dir.Track(id)
	trackInfo, err := track.InfoFile.Read()
	if nil != err {
		logger.Error().Err(err).Msg("failed to read track info file")
		return fmt.Errorf("failed to read track info file: %v", err)
	}

	trackInputFile, err := c.engine.FromPath(ctx, track.Path)
	if nil != err {
		return fmt.Errorf("failed to upload track file: %w", err)
	}

	coverInputFile, err := c.engine.FromPath(ctx, track.Cover.Path)
	if nil != err {
		return fmt.Errorf("failed to upload track cover file: %w", err)
	}

	mime, err := mimetype.DetectFile(track.Path)
	if nil != err {
		return fmt.Errorf("failed to detect mime: %w", err)
	}

	const notCollapsed = false
	caption := []message.StyledTextOption{
		styling.Blockquote(trackInfo.Caption, notCollapsed),
	}
	if sig := c.conf.Upload.Signature; len(sig) > 0 {
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
		NewSender(c.client).
		WithUploader(c.engine).
		To(c.peer).
		Media(ctx, doc)
	if nil != err {
		return fmt.Errorf("failed to send message: %w", err)
	}

	time.Sleep(c.conf.Upload.PauseDuration.Duration)

	return nil
}
