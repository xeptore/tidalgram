package tidal

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (c *Client) downloadLink(ctx context.Context, link string) (*DownloadedLink, error) {
	creds := c.auth.Credentials()

	if creds.ExpiresAt.IsZero() {
		return nil, ErrLoginRequired
	}

	if time.Now().Add(10 * time.Minute).After(creds.ExpiresAt) {
		return nil, ErrTokenRefreshRequired
	}

	info, err := c.downloadLink(ctx, link)
	if nil != err {
		if errors.Is(err, ErrUnauthorized) {
			return nil, ErrTokenRefreshRequired
		}

		return nil, fmt.Errorf("failed to download link: %v", err)
	}

	return info, nil
}
