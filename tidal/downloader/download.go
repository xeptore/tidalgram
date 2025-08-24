package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

const (
	trackAPIFormat             = "https://api.tidal.com/v1/tracks/%s"
	trackCreditsAPIFormat      = "https://api.tidal.com/v1/tracks/%s/credits" //nolint:gosec
	trackLyricsAPIFormat       = "https://api.tidal.com/v1/tracks/%s/lyrics"
	albumAPIFormat             = "https://api.tidal.com/v1/albums/%s"
	playlistAPIFormat          = "https://api.tidal.com/v1/playlists/%s"
	mixInfoURL                 = "https://listen.tidal.com/v1/pages/mix"
	trackStreamAPIFormat       = "https://api.tidal.com/v1/tracks/%s/playbackinfo"
	albumItemsCreditsAPIFormat = "https://api.tidal.com/v1/albums/%s/items/credits" //nolint:gosec
	playlistItemsAPIFormat     = "https://api.tidal.com/v1/playlists/%s/items"
	mixItemsAPIFormat          = "https://api.tidal.com/v1/mixes/%s/items"
	coverURLFormat             = "https://resources.tidal.com/images/%s/1280x1280.jpg"
	pageSize                   = 100
	maxChunkParts              = 10
	singlePartChunkSize        = 1024 * 1024
)

var (
	ErrTooManyRequests           = errors.New("too many requests")
	ErrUnsupportedArtistLinkKind = errors.New("artist link kind is not supported")
	ErrUnsupportedVideoLinkKind  = errors.New("video link kind is not supported")
)

type ListTrackMeta struct {
	AlbumID      string
	AlbumTitle   string
	ISRC         string
	Copyright    string
	Artist       string
	Artists      []types.TrackArtist
	CoverID      string
	Duration     int
	ID           string
	Title        string
	TrackNumber  int
	Version      *string
	VolumeNumber int
}

type Downloader struct {
	dir   fs.DownloadsDir
	auth  *auth.Auth
	conf  config.TidalDownloader
	cache *cache.Cache
}

func NewDownloader(
	dir fs.DownloadsDir,
	conf config.TidalDownloader,
	auth *auth.Auth,
	cache *cache.Cache,
) *Downloader {
	return &Downloader{
		dir:   dir,
		conf:  conf,
		auth:  auth,
		cache: cache,
	}
}

func (d *Downloader) Download(ctx context.Context, logger zerolog.Logger, link types.Link) error {
	switch k := link.Kind; k {
	case types.LinkKindAlbum:
		return d.album(ctx, logger, link.ID)
	case types.LinkKindTrack:
		return d.track(ctx, logger, link.ID)
	case types.LinkKindMix:
		return d.mix(ctx, logger, link.ID)
	case types.LinkKindPlaylist:
		return d.playlist(ctx, logger, link.ID)
	case types.LinkKindArtist:
		return ErrUnsupportedArtistLinkKind
	case types.LinkKindVideo:
		return ErrUnsupportedVideoLinkKind
	default:
		panic("unexpected link kind: " + strconv.Itoa(int(k)))
	}
}

func (d *Downloader) getListPagedItems(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	itemsURL string,
	page int,
) ([]byte, error) {
	logger = logger.With().Str("items_url", itemsURL).Logger()

	reqParams := make(url.Values, 3)
	reqParams.Add("countryCode", "US")
	reqParams.Add("limit", strconv.Itoa(pageSize))
	reqParams.Add("offset", strconv.Itoa(page*pageSize))

	return d.getPagedItems(ctx, logger, accessToken, itemsURL, reqParams)
}

func (d *Downloader) getPagedItems(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	itemsURL string,
	reqParams url.Values,
) (b []byte, err error) {
	reqURL, err := url.Parse(itemsURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse page items URL")
		return nil, fmt.Errorf("failed to parse page items URL: %v", err)
	}

	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get page items request")
		return nil, fmt.Errorf("failed to create get page items request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Accept", "application/json")

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetPagedTracks) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get paged tracks request")
		return nil, fmt.Errorf("failed to send get paged tracks request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get page items response body")
			err = errors.Join(err, fmt.Errorf("failed to close get page items response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return nil, fmt.Errorf("failed to read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return nil, fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return nil, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return nil, fmt.Errorf("failed to read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return nil, fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read unexpected response body")
			return nil, fmt.Errorf("failed to read unexpected response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read paged items response body")
		return nil, fmt.Errorf("failed to read paged items response body: %w", err)
	}

	return respBytes, nil
}
