package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/types"
)

func (d *Downloader) mix(ctx context.Context, logger zerolog.Logger, id string) error {
	accessToken := d.auth.Credentials().Token
	mix, err := d.getMixMeta(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to get mix meta: %w", err)
	}

	tracks, err := d.getMixTracks(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to get mix tracks: %w", err)
	}

	var (
		mixFs     = d.dir.Mix(id)
		wg, wgCtx = errgroup.WithContext(ctx)
	)

	wg.SetLimit(ratelimit.MixDownloadConcurrency)
	for i, track := range tracks {
		logger = logger.With().Int("track_index", i).Logger()

		wg.Go(func() (err error) {
			trackFs := mixFs.Track(track.ID)
			if exists, err := trackFs.Cover.Exists(); nil != err {
				logger.Error().Err(err).Msg("Failed to check if track cover exists")
				return fmt.Errorf("failed to check if track cover exists: %v", err)
			} else if !exists {
				coverBytes, err := d.getCover(wgCtx, logger, accessToken, track.CoverID)
				if nil != err {
					return fmt.Errorf("failed to get track cover: %w", err)
				}
				if err := trackFs.Cover.Write(coverBytes); nil != err {
					logger.Error().Err(err).Msg("Failed to write track cover")
					return fmt.Errorf("failed to write track cover: %v", err)
				}
			}

			if exists, err := trackFs.Exists(); nil != err {
				logger.Error().Err(err).Msg("Failed to check if track exists")
				return fmt.Errorf("failed to check if track exists: %v", err)
			} else if exists {
				return nil
			}
			defer func() {
				if nil != err {
					if removeErr := trackFs.Remove(); nil != removeErr {
						if !errors.Is(err, os.ErrNotExist) {
							logger.Error().Err(removeErr).Msg("Failed to remove mix track file")
							err = errors.Join(err, fmt.Errorf("failed to remove mix track file: %v", removeErr))
						}
					}
				}
			}()

			trackCredits, err := d.getTrackCredits(wgCtx, logger, accessToken, track.ID)
			if nil != err {
				return fmt.Errorf("failed to get track credits: %w", err)
			}

			trackLyrics, err := d.downloadTrackLyrics(wgCtx, logger, accessToken, track.ID)
			if nil != err {
				return fmt.Errorf("failed to download track lyrics: %w", err)
			}

			ext, err := d.downloadTrack(wgCtx, logger, accessToken, track.ID, trackFs.Path)
			if nil != err {
				return fmt.Errorf("failed to download track: %w", err)
			}

			album, err := d.getAlbumMeta(wgCtx, logger, accessToken, track.AlbumID)
			if nil != err {
				return fmt.Errorf("failed to get album meta: %w", err)
			}

			attrs := TrackEmbeddedAttrs{
				LeadArtist:   track.Artist,
				Album:        track.AlbumTitle,
				AlbumArtist:  album.Artist,
				Artists:      track.Artists,
				Copyright:    track.Copyright,
				CoverPath:    trackFs.Cover.Path,
				ISRC:         track.ISRC,
				ReleaseDate:  album.ReleaseDate,
				Title:        track.Title,
				TrackNumber:  track.TrackNumber,
				TotalTracks:  album.TotalTracks,
				Version:      track.Version,
				VolumeNumber: track.VolumeNumber,
				TotalVolumes: album.TotalVolumes,
				Credits:      *trackCredits,
				Lyrics:       trackLyrics,
				Ext:          ext,
			}
			if err := embedTrackAttributes(wgCtx, logger, trackFs.Path, attrs); nil != err {
				return fmt.Errorf("failed to embed track attributes: %v", err)
			}

			info := types.StoredTrack{
				Track: types.Track{
					Artists:  track.Artists,
					Title:    track.Title,
					Duration: track.Duration,
					Version:  track.Version,
					CoverID:  track.CoverID,
					Ext:      ext,
				},
				Caption: trackCaption(*album),
			}
			if err := trackFs.InfoFile.Write(info); nil != err {
				logger.Error().Err(err).Msg("Failed to write track info")
				return fmt.Errorf("failed to write track info: %v", err)
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("failed to wait for track download workers: %w", err)
	}

	info := types.StoredMix{
		Caption:  mix.Title,
		TrackIDs: lo.Map(tracks, func(t ListTrackMeta, _ int) string { return t.ID }),
	}
	if err := mixFs.InfoFile.Write(info); nil != err {
		logger.Error().Err(err).Msg("Failed to write mix info")
		return fmt.Errorf("failed to write mix info: %v", err)
	}

	return nil
}

func (d *Downloader) getMixMeta(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) (m *MixMeta, err error) {
	mixInfoURL := "https://listen.tidal.com/v1/pages/mix"
	reqURL, err := url.Parse(mixInfoURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse mix info URL")
		return nil, fmt.Errorf("failed to parse mix info URL: %v", err)
	}

	reqParams := make(url.Values, 4)
	reqParams.Add("mixId", id)
	reqParams.Add("countryCode", "US")
	reqParams.Add("locale", "en_US")
	reqParams.Add("deviceType", "BROWSER")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get mix info request")
		return nil, fmt.Errorf("failed to create get mix info request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add(
		"User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:132.0) Gecko/20100101 Firefox/132.0",
	)
	req.Header.Add("Accept", "application/json")

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetMixInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get mix info request")
		return nil, fmt.Errorf("failed to send get mix info request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get mix info response body")
			err = errors.Join(err, fmt.Errorf("failed to close get mix info response body: %v", closeErr))
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
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, fmt.Errorf("failed to read 200 response body: %w", err)
	}

	if !gjson.ValidBytes(respBytes) {
		logger.Error().Bytes("response_body", respBytes).Msg("Invalid mix info response json")
		return nil, fmt.Errorf("invalid mix info response json: %v", err)
	}

	var title string
	switch titleKey := gjson.GetBytes(respBytes, "title"); titleKey.Type { //nolint:exhaustive
	case gjson.String:
		title = titleKey.Str
	default:
		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected mix info response")
		return nil, fmt.Errorf("unexpected mix info response: %s", string(respBytes))
	}

	return &MixMeta{Title: title}, nil
}

type MixMeta struct {
	Title string
}

func (d *Downloader) getMixTracks(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) ([]ListTrackMeta, error) {
	var tracks []ListTrackMeta

	for i := 0; ; i++ {
		pageTracks, rem, err := d.mixTracksPage(ctx, logger, accessToken, id, i)
		if nil != err {
			return nil, fmt.Errorf("failed to get mix tracks page: %w", err)
		}

		if rem == 0 {
			break
		}

		tracks = append(tracks, pageTracks...)
	}

	return tracks, nil
}

func (d *Downloader) mixTracksPage(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
	page int,
) (ts []ListTrackMeta, rem int, err error) {
	mixURL, err := url.JoinPath(fmt.Sprintf(mixItemsAPIFormat, id))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join mix URL with id")
		return nil, 0, fmt.Errorf("failed to create mix URL: %v", err)
	}

	respBytes, err := d.getListPagedItems(ctx, logger, accessToken, mixURL, page)
	if nil != err {
		return nil, 0, fmt.Errorf("failed to get mix tracks page: %w", err)
	}

	var respBody struct {
		TotalNumberOfItems int `json:"totalNumberOfItems"`
		Items              []struct {
			Type string `json:"type"`
			Item struct {
				ID           int    `json:"id"`
				StreamReady  bool   `json:"streamReady"`
				TrackNumber  int    `json:"trackNumber"`
				VolumeNumber int    `json:"volumeNumber"`
				Title        string `json:"title"`
				Copyright    string `json:"copyright"`
				ISRC         string `json:"isrc"`
				Duration     int    `json:"duration"`
				Artist       struct {
					Name string `json:"name"`
				} `json:"artist"`
				Artists []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"artists"`
				Album struct {
					ID    int    `json:"id"`
					Cover string `json:"cover"`
					Title string `json:"title"`
				} `json:"album"`
				Version *string `json:"version"`
			} `json:"item"`
		} `json:"items"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode mix response")
		return nil, 0, fmt.Errorf("failed to decode mix response: %v", err)
	}

	thisPageItemsCount := len(respBody.Items)
	if thisPageItemsCount == 0 {
		return nil, 0, nil
	}

	for _, v := range respBody.Items {
		if v.Type != pageItemTypeTrack || !v.Item.StreamReady {
			continue
		}

		artists := make([]types.TrackArtist, len(v.Item.Artists))
		for i, a := range v.Item.Artists {
			switch a.Type {
			case types.ArtistTypeMain, types.ArtistTypeFeatured:
			default:
				logger.Error().Str("artist_type", a.Type).Msg("Unexpected mix track artist type")
				return nil, 0, fmt.Errorf("unexpected mix track artist type: %s", a.Type)
			}
			artists[i] = types.TrackArtist{Name: a.Name, Type: a.Type}
		}

		t := ListTrackMeta{
			AlbumID:      strconv.Itoa(v.Item.Album.ID),
			AlbumTitle:   v.Item.Album.Title,
			ISRC:         v.Item.ISRC,
			Copyright:    v.Item.Copyright,
			Artist:       v.Item.Artist.Name,
			Artists:      artists,
			CoverID:      v.Item.Album.Cover,
			Duration:     v.Item.Duration,
			ID:           strconv.Itoa(v.Item.ID),
			Title:        v.Item.Title,
			TrackNumber:  v.Item.TrackNumber,
			Version:      v.Item.Version,
			VolumeNumber: v.Item.VolumeNumber,
		}
		ts = append(ts, t)
	}

	return ts, respBody.TotalNumberOfItems - (thisPageItemsCount + page*pageSize), nil
}
