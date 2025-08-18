package tidal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sethvargo/go-retry"

	"github.com/xeptore/tidalgram/tidal/auth"
)

type Client struct {
	auth *auth.Auth
}

func NewClient(credsDir string) (*Client, error) {
	a, err := auth.New(credsDir)
	if nil != err {
		return nil, fmt.Errorf("failed to create auth: %w", err)
	}

	return &Client{
		auth: a,
	}, nil
}

var (
	ErrTokenRefreshRequired = errors.New("auth token refresh required")
	ErrTokenRefreshed       = errors.New("auth token refreshed")
	ErrLoginRequired        = errors.New("login required")
	ErrUnauthorized         = auth.ErrUnauthorized
)

type DownloadedLink struct{}

func (c *Client) DownloadLink(ctx context.Context, link string) (*DownloadedLink, error) {
	res, err := retry.DoValue(
		ctx,
		retry.WithMaxRetries(7, retry.NewFibonacci(1*time.Second)),
		func(ctx context.Context) (*DownloadedLink, error) {
			res, err := c.downloadLink(ctx, link)
			if nil != err {
				if errors.Is(err, ErrLoginRequired) {
					return nil, ErrLoginRequired
				}
				if errors.Is(err, ErrTokenRefreshRequired) {
					if err := c.auth.TryRefreshToken(ctx); nil != err {
						if errors.Is(err, auth.ErrTokenRefreshInProgress) {
							return nil, retry.RetryableError(auth.ErrTokenRefreshInProgress)
						}
						if errors.Is(err, auth.ErrUnauthorized) {
							return nil, ErrLoginRequired
						}

						return nil, fmt.Errorf("failed to refresh token: %v", err)
					}

					return nil, retry.RetryableError(ErrTokenRefreshed)
				}

				return nil, fmt.Errorf("failed to download link: %v", err)
			}

			return res, nil
		},
	)
	if nil != err {
		if errors.Is(err, ErrTokenRefreshed) {
			// Give it another chance to download the link even when max retries are reached.
			return c.downloadLink(ctx, link)
		}

		return nil, fmt.Errorf("failed to download link: %v", err)
	}

	return res, nil
}
