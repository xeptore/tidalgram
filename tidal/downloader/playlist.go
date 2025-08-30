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
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/types"
)

func (d *Downloader) playlist(ctx context.Context, logger zerolog.Logger, id string) error {
	accessToken := d.auth.Credentials().Token
	playlist, err := d.getPlaylistMeta(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("get playlist meta: %w", err)
	}

	tracks, err := d.getPlaylistTracks(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("get playlist tracks: %w", err)
	}

	var (
		playlistFs = d.dir.Playlist(id)
		wg, wgctx  = errgroup.WithContext(ctx)
	)

	wg.SetLimit(d.conf.Concurrency.PlaylistTracks)

	for i, track := range tracks {
		logger = logger.With().Int("track_index", i).Logger()

		wg.Go(func() (err error) {
			trackFs := playlistFs.Track(track.ID)
			if exists, err := trackFs.Cover.Exists(); nil != err {
				logger.Error().Err(err).Msg("Failed to check if track cover exists")
				return fmt.Errorf("check if track cover exists: %v", err)
			} else if !exists {
				coverBytes, err := d.getCover(wgctx, logger, accessToken, track.CoverID)
				if nil != err {
					return fmt.Errorf("get track cover: %w", err)
				}
				if err := trackFs.Cover.Write(coverBytes); nil != err {
					logger.Error().Err(err).Msg("Failed to write track cover")
					return fmt.Errorf("write track cover: %v", err)
				}
			}

			if exists, err := trackFs.Exists(); nil != err {
				logger.Error().Err(err).Msg("Failed to check if track file exists")
				return fmt.Errorf("check if track file exists: %v", err)
			} else if exists {
				return nil
			}
			defer func() {
				if nil != err {
					if removeErr := trackFs.Remove(); nil != removeErr {
						if !errors.Is(err, os.ErrNotExist) {
							logger.Error().Err(removeErr).Msg("Failed to remove playlist track file")
							err = errors.Join(err, fmt.Errorf("remove playlist track file: %v", removeErr))
						}
					}
				}
			}()

			trackCredits, err := d.getTrackCredits(wgctx, logger, accessToken, track.ID)
			if nil != err {
				return fmt.Errorf("get track credits: %w", err)
			}

			trackLyrics, err := d.downloadTrackLyrics(wgctx, logger, accessToken, track.ID)
			if nil != err {
				return fmt.Errorf("download track lyrics: %w", err)
			}

			ext, err := d.downloadTrack(wgctx, logger, accessToken, track.ID, trackFs.Path)
			if nil != err {
				return fmt.Errorf("download track: %w", err)
			}

			album, err := d.getAlbumMeta(wgctx, logger, accessToken, track.AlbumID)
			if nil != err {
				return fmt.Errorf("get album meta: %w", err)
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
			if err := embedTrackAttributes(wgctx, logger, trackFs.Path, attrs); nil != err {
				return fmt.Errorf("embed track attributes: %v", err)
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
				Caption: trackCaption(album.Title, album.ReleaseDate),
			}
			if err := trackFs.InfoFile.Write(info); nil != err {
				logger.Error().Err(err).Msg("Failed to write track info file")
				return fmt.Errorf("write track info file: %v", err)
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("wait for track download workers: %w", err)
	}

	info := types.StoredPlaylist{
		Caption:  fmt.Sprintf("%s (%d - %d)", playlist.Title, playlist.StartYear, playlist.EndYear),
		TrackIDs: lo.Map(tracks, func(t ListTrackMeta, _ int) string { return t.ID }),
	}
	if err := playlistFs.InfoFile.Write(info); nil != err {
		logger.Error().Err(err).Msg("Failed to write playlist info file")
		return fmt.Errorf("write playlist info file: %v", err)
	}

	return nil
}

func (d *Downloader) getPlaylistMeta(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) (m *PlaylistMeta, err error) {
	playlistURL, err := url.JoinPath(fmt.Sprintf(playlistAPIFormat, id))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join playlist base URL with playlist id")
		return nil, fmt.Errorf("join playlist base URL with playlist id: %v", err)
	}

	reqURL, err := url.Parse(playlistURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse playlist URL")
		return nil, fmt.Errorf("parse playlist URL: %v", err)
	}

	queryParams := make(url.Values, 1)
	queryParams.Add("countryCode", "US")
	reqURL.RawQuery = queryParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get playlist info request")
		return nil, fmt.Errorf("create get playlist info request: %w", err)
	}

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetPlaylistInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get playlist info request")
		return nil, fmt.Errorf("send get playlist info request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get playlist info response body")
			err = errors.Join(err, fmt.Errorf("close get playlist info response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return nil, fmt.Errorf("read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return nil, fmt.Errorf("check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return nil, fmt.Errorf("check if 401 response is token invalid: %v", err)
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
			return nil, fmt.Errorf("read 403 response body: %w", err)
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return nil, fmt.Errorf("check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return nil, fmt.Errorf("read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, fmt.Errorf("read 200 response body: %w", err)
	}

	var respBody struct {
		Title       string `json:"title"`
		Created     string `json:"created"`
		LastUpdated string `json:"lastUpdated"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode 200 response body")
		return nil, fmt.Errorf("decode 200 response body: %w", err)
	}

	const dateLayout = "2006-01-02T15:04:05.000-0700"
	createdAt, err := time.Parse(dateLayout, respBody.Created)
	if nil != err {
		logger.Error().Err(err).Str("created", respBody.Created).Msg("Failed to parse playlist created date")
		return nil, fmt.Errorf("parse playlist created date: %v", err)
	}

	lastUpdatedAt, err := time.Parse(dateLayout, respBody.LastUpdated)
	if nil != err {
		logger.Error().Err(err).Str("last_updated", respBody.LastUpdated).Msg("Failed to parse playlist last updated date")
		return nil, fmt.Errorf("parse playlist last updated date: %v", err)
	}

	return &PlaylistMeta{
		Title:     respBody.Title,
		StartYear: createdAt.Year(),
		EndYear:   lastUpdatedAt.Year(),
	}, nil
}

type PlaylistMeta struct {
	Title     string
	StartYear int
	EndYear   int
}

func (d *Downloader) getPlaylistTracks(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) ([]ListTrackMeta, error) {
	var tracks []ListTrackMeta
	for i := 0; ; i++ {
		pageTracks, rem, err := d.playlistTracksPage(ctx, logger, accessToken, id, i)
		if nil != err {
			return nil, fmt.Errorf("get playlist tracks page: %w", err)
		}

		tracks = append(tracks, pageTracks...)

		if rem == 0 {
			break
		}
	}

	return tracks, nil
}

const pageItemTypeTrack = "track"

func (d *Downloader) playlistTracksPage(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
	page int,
) (ts []ListTrackMeta, rem int, err error) {
	playlistURL, err := url.JoinPath(fmt.Sprintf(playlistItemsAPIFormat, id))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join playlist URL with id")
		return nil, 0, fmt.Errorf("join playlist URL with id: %v", err)
	}

	respBytes, err := d.getListPagedItems(ctx, logger, accessToken, playlistURL, page)
	if nil != err {
		return nil, 0, fmt.Errorf("get playlist tracks page: %w", err)
	}

	var respBody struct {
		TotalNumberOfItems int `json:"totalNumberOfItems"`
		Items              []struct {
			Type string `json:"type"`
			Cut  any    `json:"any"`
			Item struct {
				ID           int    `json:"id"`
				StreamReady  bool   `json:"streamReady"`
				TrackNumber  int    `json:"trackNumber"`
				VolumeNumber int    `json:"volumeNumber"`
				Title        string `json:"title"`
				ISRC         string `json:"isrc"`
				Copyright    string `json:"copyright"`
				Duration     int    `json:"duration"`
				Artist       struct {
					Name string `json:"name"`
				} `json:"artist"`
				Artists []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"artists"`
				Album struct {
					ID      int    `json:"id"`
					CoverID string `json:"cover"`
					Title   string `json:"title"`
				} `json:"album"`
				Version *string `json:"version"`
			} `json:"item"`
		} `json:"items"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode playlist tracks response")
		return nil, 0, fmt.Errorf("decode playlist tracks response: %v", err)
	}

	thisPageItemsCount := len(respBody.Items)
	if thisPageItemsCount == 0 {
		return nil, 0, nil
	}

	for _, v := range respBody.Items {
		if v.Type != pageItemTypeTrack || !v.Item.StreamReady {
			continue
		}
		if v.Cut != nil {
			return nil, 0, errors.New("cut items are not supported")
		}

		artists := make([]types.TrackArtist, len(v.Item.Artists))
		for i, a := range v.Item.Artists {
			switch a.Type {
			case types.ArtistTypeMain, types.ArtistTypeFeatured:
			default:
				return nil, 0, fmt.Errorf("unexpected playlist track artist type: %s", a.Type)
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
			CoverID:      v.Item.Album.CoverID,
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
