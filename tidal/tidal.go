package tidal

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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
	DownloadsDirFs fs.DownloadsDir
	dl             *downloader.Downloader
}

func NewClient(logger zerolog.Logger, credsDir, dlDir string, conf config.Tidal) (*Client, error) {
	a, err := auth.New(logger, credsDir)
	if nil != err {
		return nil, fmt.Errorf("create auth: %v", err)
	}

	var (
		c       = cache.New()
		dlDirFs = fs.DownloadsDirFrom(dlDir)
		dl      = downloader.NewDownloader(dlDirFs, conf.Downloader, a, c)
	)

	return &Client{
		auth:           a,
		dl:             dl,
		DownloadsDirFs: dlDirFs,
	}, nil
}

var (
	ErrTokenRefreshRequired      = errors.New("auth token refresh required")
	ErrTokenRefreshed            = errors.New("auth token refreshed")
	ErrLoginRequired             = errors.New("login required")
	ErrUnauthorized              = auth.ErrUnauthorized
	ErrLoginLinkExpired          = auth.ErrLoginLinkExpired
	ErrUnsupportedArtistLinkKind = downloader.ErrUnsupportedArtistLinkKind
	ErrUnsupportedVideoLinkKind  = downloader.ErrUnsupportedVideoLinkKind
)

func (c *Client) TryDownloadLink(ctx context.Context, logger zerolog.Logger, link types.Link) error {
	err := retry.Do(
		ctx,
		retry.WithMaxRetries(3, retry.NewFibonacci(1*time.Second)),
		func(ctx context.Context) error {
			if err := c.downloadLink(ctx, logger, link); nil != err {
				if errors.Is(err, context.Canceled) {
					return context.Canceled
				}

				if errors.Is(err, context.DeadlineExceeded) {
					return retry.RetryableError(context.DeadlineExceeded)
				}

				if errors.Is(err, ErrTokenRefreshRequired) {
					if err := c.auth.RefreshToken(ctx, logger); nil != err {
						if errors.Is(err, context.Canceled) {
							return context.Canceled
						}

						if errors.Is(err, context.DeadlineExceeded) {
							return retry.RetryableError(context.DeadlineExceeded)
						}

						if errors.Is(err, auth.ErrUnauthorized) {
							return ErrLoginRequired
						}

						return fmt.Errorf("refresh token: %w", err)
					}

					return retry.RetryableError(ErrTokenRefreshed)
				}

				if errors.Is(err, downloader.ErrUnsupportedArtistLinkKind) {
					return ErrUnsupportedArtistLinkKind
				}

				if errors.Is(err, downloader.ErrUnsupportedVideoLinkKind) {
					return ErrUnsupportedVideoLinkKind
				}

				return err
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
		return fmt.Errorf("download link after retries: %w", err)
	}

	return nil
}

func (c *Client) TryInitiateLoginFlow(
	ctx context.Context,
	logger zerolog.Logger,
) (*auth.LoginLink, <-chan error, error) {
	link, wait, err := c.auth.InitiateLoginFlow(ctx, logger)
	if nil != err {
		return nil, nil, fmt.Errorf("initiate login flow: %w", err)
	}

	return link, wait, nil
}

func ParseLink(l string) types.Link {
	u, err := url.Parse(l)
	must.NilErr(err)

	var (
		id   string
		kind types.LinkKind
	)
	pathParts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3)

	if len(pathParts) >= 1 && pathParts[0] == "browse" {
		pathParts = pathParts[1:]
	} else if len(pathParts) >= 3 && pathParts[2] == "u" {
		pathParts = pathParts[:2]
	}

	if len(pathParts) < 2 {
		panic(fmt.Sprintf("unexpected link format: not enough path parts in %q", l))
	}
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
		return fmt.Errorf("download link: %w", err)
	}

	return nil
}
