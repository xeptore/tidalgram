package tidal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xeptore/tidalgram/tidal/downloader"
	"github.com/xeptore/tidalgram/tidal/types"
)

func (c *Client) downloadLink(ctx context.Context, link types.Link) error {
	creds := c.auth.Credentials()

	if creds.ExpiresAt.IsZero() {
		return ErrLoginRequired
	}

	if time.Now().Add(10 * time.Minute).After(creds.ExpiresAt) {
		return ErrTokenRefreshRequired
	}

	if err := c.dl.Download(ctx, link); nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, downloader.ErrUnsupportedArtistLinkKind) {
			return downloader.ErrUnsupportedArtistLinkKind
		}

		if errors.Is(err, downloader.ErrUnsupportedVideoLinkKind) {
			return downloader.ErrUnsupportedVideoLinkKind
		}

		if errors.Is(err, ErrUnauthorized) {
			return ErrTokenRefreshRequired
		}

		return fmt.Errorf("failed to download link: %v", err)
	}

	return nil
}
