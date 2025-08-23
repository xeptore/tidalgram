package tidal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sethvargo/go-retry"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/must"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/downloader"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

type Client struct {
	auth           *auth.Auth
	loginSem       chan struct{}
	DownloadsDirFs fs.DownloadsDir
	dl             *downloader.Downloader
	downloadSem    chan struct{}
}

func NewClient(logger zerolog.Logger, credsDir, dlDir string, conf config.Tidal) (*Client, error) {
	a, err := auth.New(logger, credsDir)
	if nil != err {
		return nil, fmt.Errorf("failed to create auth: %v", err)
	}

	var (
		c       = cache.New()
		dlDirFs = fs.DownloadsDirFrom(dlDir)
		dl      = downloader.NewDownloader(dlDirFs, conf.Downloader, a, c)
	)

	return &Client{
		auth:           a,
		dl:             dl,
		loginSem:       make(chan struct{}, 1),
		downloadSem:    make(chan struct{}, 1),
		DownloadsDirFs: dlDirFs,
	}, nil
}

var (
	ErrTokenRefreshRequired      = errors.New("auth token refresh required")
	ErrTokenRefreshed            = errors.New("auth token refreshed")
	ErrLoginRequired             = errors.New("login required")
	ErrUnauthorized              = auth.ErrUnauthorized
	ErrDownloadInProgress        = errors.New("download in progress")
	ErrLoginInProgress           = auth.ErrLoginInProgress
	ErrLoginLinkExpired          = auth.ErrLoginLinkExpired
	ErrUnsupportedArtistLinkKind = downloader.ErrUnsupportedArtistLinkKind
	ErrUnsupportedVideoLinkKind  = downloader.ErrUnsupportedVideoLinkKind
)

func (c *Client) TryDownloadLink(ctx context.Context, logger zerolog.Logger, link types.Link) error {
	select {
	case c.downloadSem <- struct{}{}:
		logger.Debug().Msg("Downloading link")
		defer func() { <-c.downloadSem }()
		err := retry.Do(
			ctx,
			retry.WithMaxRetries(7, retry.NewFibonacci(1*time.Second)),
			func(ctx context.Context) error {
				if err := c.downloadLink(ctx, logger, link); nil != err {
					if errors.Is(err, context.DeadlineExceeded) {
						return retry.RetryableError(context.DeadlineExceeded)
					}

					if errors.Is(err, ErrTokenRefreshRequired) {
						if err := c.auth.RefreshToken(ctx, logger); nil != err {
							if errors.Is(err, context.DeadlineExceeded) {
								return retry.RetryableError(context.DeadlineExceeded)
							}

							if errors.Is(err, auth.ErrUnauthorized) {
								return ErrLoginRequired
							}

							return fmt.Errorf("failed to refresh token: %w", err)
						}

						return retry.RetryableError(ErrTokenRefreshed)
					}

					if errors.Is(err, downloader.ErrUnsupportedArtistLinkKind) {
						return ErrUnsupportedArtistLinkKind
					}

					if errors.Is(err, downloader.ErrUnsupportedVideoLinkKind) {
						return ErrUnsupportedVideoLinkKind
					}

					return fmt.Errorf("failed to download link: %w", err)
				}

				return nil
			},
		)
		if nil != err {
			if errors.Is(err, ErrTokenRefreshed) {
				// Give it another chance to download the link even when max retries are reached.
				return c.downloadLink(ctx, logger, link)
			}

			// Make all error kinds handled in the retry loop above available to the caller as we want to handle them.
			return fmt.Errorf("failed to download link after retries: %w", err)
		}

		return nil
	default:
		logger.Debug().Msg("Another download in progress")
		return ErrDownloadInProgress
	}
}

func (c *Client) TryInitiateLoginFlow(
	ctx context.Context,
	logger zerolog.Logger,
) (*auth.LoginLink, <-chan error, error) {
	select {
	case c.loginSem <- struct{}{}:
		logger.Debug().Msg("Initiating login flow")
		defer func() { <-c.loginSem }()
		link, wait, err := c.auth.InitiateLoginFlow(ctx, logger)
		if nil != err {
			return nil, nil, fmt.Errorf("failed to initiate login flow: %w", err)
		}

		return link, wait, nil
	default:
		logger.Debug().Msg("Another login in progress")
		return nil, nil, ErrLoginInProgress
	}
}

func ParseLink(l string) types.Link {
	u, err := url.Parse(l)
	must.NilErr(err)

	var (
		id   string
		kind types.LinkKind
	)
	switch pathParts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3); len(pathParts) {
	case 2:
		id = pathParts[1]
		switch k := pathParts[0]; k {
		case "mix":
			kind = types.LinkKindMix
		case "playlist":
			kind = types.LinkKindPlaylist
		case "album":
			kind = types.LinkKindAlbum
		case "track":
			kind = types.LinkKindTrack
		case "artist":
			kind = types.LinkKindArtist
		case "video":
			kind = types.LinkKindVideo
		default:
			panic("unexpected link media type: " + k)
		}
	case 3:
		id = pathParts[2]
		switch k := pathParts[1]; k {
		case "mix":
			kind = types.LinkKindMix
		case "playlist":
			kind = types.LinkKindPlaylist
		case "album":
			kind = types.LinkKindAlbum
		case "track":
			kind = types.LinkKindTrack
		case "artist":
			kind = types.LinkKindArtist
		case "video":
			kind = types.LinkKindVideo
		default:
			panic("unexpected link media type: " + k)
		}
	default:
		panic("unexpected link parts length: " + strconv.Itoa(len(pathParts)))
	}

	return types.Link{Kind: kind, ID: id}
}

func (c *Client) downloadLink(ctx context.Context, logger zerolog.Logger, link types.Link) error {
	creds := c.auth.Credentials()

	if creds.ExpiresAt.IsZero() {
		return ErrLoginRequired
	}

	if time.Now().Add(10 * time.Minute).After(creds.ExpiresAt) {
		return ErrTokenRefreshRequired
	}

	if err := c.dl.Download(ctx, logger, link); nil != err {
		return fmt.Errorf("failed to download link: %w", err)
	}

	return nil
}
