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

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/types"
)

type AlbumTrackMeta struct {
	Artist       string
	Artists      []types.TrackArtist
	Duration     int
	ID           string
	Title        string
	Copyright    string
	ISRC         string
	TrackNumber  int
	Version      *string
	VolumeNumber int
	Credits      types.TrackCredits
}

func (d *Downloader) album(ctx context.Context, logger zerolog.Logger, id string) error {
	logger.Debug().Msg("Downloading album")

	accessToken := d.auth.Credentials().Token
	album, err := d.getAlbumMeta(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to get album meta: %w", err)
	}

	albumFs := d.dir.Album(id)
	if exists, err := albumFs.Cover.Exists(); nil != err {
		logger.Error().Err(err).Msg("Failed to check if track cover file exists")
		return fmt.Errorf("failed to check if track cover file exists: %v", err)
	} else if !exists {
		coverBytes, err := d.getCover(ctx, logger, accessToken, album.CoverID)
		if nil != err {
			return fmt.Errorf("failed to get album cover: %w", err)
		}
		if err := albumFs.Cover.Write(coverBytes); nil != err {
			logger.Error().Err(err).Msg("Failed to write album cover")
			return fmt.Errorf("failed to write album cover: %v", err)
		}
	}

	volumes, err := d.getAlbumVolumes(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to get album volumes: %w", err)
	}

	for _, volTracks := range volumes {
		for _, track := range volTracks {
			d.cache.TrackCredits.Set(track.ID, &track.Credits, cache.DefaultTrackCreditsTTL)
		}
	}

	var (
		wg, wgctx           = errgroup.WithContext(ctx)
		albumVolumeTrackIDs = make([][]string, len(volumes))
	)

	wg.SetLimit(ratelimit.AlbumDownloadConcurrency)
	for i, tracks := range volumes {
		albumVolumeTrackIDs[i] = lo.Map(tracks, func(t AlbumTrackMeta, _ int) string { return t.ID })

		volNum := i + 1
		for idx, track := range tracks {
			logger = logger.With().Int("track_index", idx).Str("track_id", track.ID).Logger()

			wg.Go(func() (err error) {
				trackFs := albumFs.Track(volNum, track.ID)
				if exists, err := trackFs.Exists(); nil != err {
					logger.Error().Err(err).Msg("Failed to check if track file exists")
					return fmt.Errorf("failed to check if track file exists: %v", err)
				} else if exists {
					return nil
				}
				defer func() {
					if nil != err {
						if removeErr := trackFs.Remove(); nil != removeErr {
							if !errors.Is(removeErr, os.ErrNotExist) {
								logger.Error().Err(removeErr).Msg("Failed to remove track file")
								err = errors.Join(err, fmt.Errorf("failed to remove track file: %v", removeErr))
							}
						}
					}
				}()

				trackLyrics, err := d.downloadTrackLyrics(wgctx, logger, accessToken, track.ID)
				if nil != err {
					return fmt.Errorf("failed to download track lyrics: %w", err)
				}

				ext, err := d.downloadTrack(wgctx, logger, accessToken, track.ID, trackFs.Path)
				if nil != err {
					return fmt.Errorf("failed to download track: %w", err)
				}

				attrs := TrackEmbeddedAttrs{
					LeadArtist:   track.Artist,
					Album:        album.Title,
					AlbumArtist:  album.Artist,
					Artists:      track.Artists,
					Copyright:    track.Copyright,
					CoverPath:    albumFs.Cover.Path,
					ISRC:         track.ISRC,
					ReleaseDate:  album.ReleaseDate,
					Title:        track.Title,
					TrackNumber:  track.TrackNumber,
					TotalTracks:  album.TotalTracks,
					Version:      track.Version,
					VolumeNumber: track.VolumeNumber,
					TotalVolumes: album.TotalVolumes,
					Credits:      track.Credits,
					Lyrics:       trackLyrics,
					Ext:          ext,
				}
				if err := embedTrackAttributes(wgctx, logger, trackFs.Path, attrs); nil != err {
					return fmt.Errorf("failed to embed track attributes: %v", err)
				}

				info := types.StoredAlbumTrack{
					Track: types.Track{
						Artists:  track.Artists,
						Title:    track.Title,
						Duration: track.Duration,
						Version:  track.Version,
						CoverID:  album.CoverID,
						Ext:      ext,
					},
					Index: idx,
				}
				if err := trackFs.InfoFile.Write(info); nil != err {
					logger.Error().Err(err).Msg("Failed to write track info file")
					return fmt.Errorf("failed to write track info file: %v", err)
				}

				return nil
			})
		}
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("failed to wait for track download workers: %w", err)
	}

	info := types.StoredAlbum{
		Caption: fmt.Sprintf(
			"%s (%s)",
			album.Title,
			album.ReleaseDate.Format(types.ReleaseDateLayout),
		),
		VolumeTrackIDs: albumVolumeTrackIDs,
	}
	if err := albumFs.InfoFile.Write(info); nil != err {
		logger.Error().Err(err).Msg("Failed to write album info file")
		return fmt.Errorf("failed to write album info file: %v", err)
	}

	return nil
}

func (d *Downloader) getAlbumVolumes(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken,
	id string,
) ([][]AlbumTrackMeta, error) {
	var (
		tracks              [][]AlbumTrackMeta
		currentVolumeTracks []AlbumTrackMeta
		currentVolume       = 1
	)

	for i := 0; ; i++ {
		pageTracks, rem, err := d.albumTracksPage(ctx, logger, accessToken, id, i)
		if nil != err {
			return nil, fmt.Errorf("failed to get album tracks page: %w", err)
		}

		for _, track := range pageTracks {
			switch track.VolumeNumber {
			case currentVolume:
				currentVolumeTracks = append(currentVolumeTracks, track)
			case currentVolume + 1:
				tracks = append(tracks, currentVolumeTracks)
				currentVolumeTracks = []AlbumTrackMeta{track}
				currentVolume++
			default:
				return nil, fmt.Errorf("unexpected volume number: %d", track.VolumeNumber)
			}
		}

		if rem == 0 {
			break
		}
	}

	tracks = append(tracks, currentVolumeTracks)

	return tracks, nil
}

func (d *Downloader) albumTracksPage(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken,
	id string,
	page int,
) (ts []AlbumTrackMeta, rem int, err error) {
	logger = logger.With().Str("album_id", id).Int("page", page).Logger()

	albumURL, err := url.JoinPath(fmt.Sprintf(albumItemsCreditsAPIFormat, id))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join album tracks credits URL with id")
		return nil, 0, fmt.Errorf("failed to join album tracks credits URL with id: %v", err)
	}

	respBytes, err := d.getAlbumPagedItems(ctx, logger, accessToken, albumURL, page)
	if nil != err {
		return nil, 0, fmt.Errorf("failed to get album paged items: %w", err)
	}

	var respBody struct {
		TotalNumberOfItems int `json:"totalNumberOfItems"`
		Items              []struct {
			Type    string               `json:"type"`
			Credits TrackCreditsResponse `json:"credits"`
			Item    struct {
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
				} `json:"album"`
				Version *string `json:"version"`
			} `json:"item"`
		} `json:"items"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode album items page response")
		return nil, 0, fmt.Errorf("failed to decode album items page response: %v", err)
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
				return nil, 0, fmt.Errorf("unexpected artist type: %s", a.Type)
			}
			artists[i] = types.TrackArtist{Name: a.Name, Type: a.Type}
		}

		t := AlbumTrackMeta{
			Artist:       v.Item.Artist.Name,
			Artists:      artists,
			Duration:     v.Item.Duration,
			ID:           strconv.Itoa(v.Item.ID),
			Title:        v.Item.Title,
			Copyright:    v.Item.Copyright,
			ISRC:         v.Item.ISRC,
			TrackNumber:  v.Item.TrackNumber,
			Version:      v.Item.Version,
			VolumeNumber: v.Item.VolumeNumber,
			Credits:      v.Credits.toTrackCredits(),
		}
		ts = append(ts, t)
	}

	return ts, respBody.TotalNumberOfItems - (thisPageItemsCount + page*pageSize), nil
}

func (d *Downloader) getAlbumPagedItems(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken,
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

func (d *Downloader) getAlbumMeta(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken,
	id string,
) (*types.AlbumMeta, error) {
	cachedAlbumMeta, err := d.cache.AlbumsMeta.Fetch(
		id,
		cache.DefaultAlbumTTL,
		func() (*types.AlbumMeta, error) { return d.downloadAlbumMeta(ctx, logger, accessToken, id) },
	)
	if nil != err {
		return nil, fmt.Errorf("failed to download album meta: %w", err)
	}

	return cachedAlbumMeta.Value(), nil
}

func (d *Downloader) downloadAlbumMeta(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken,
	id string,
) (m *types.AlbumMeta, err error) {
	albumURL, err := url.JoinPath(fmt.Sprintf(albumAPIFormat, id))
	if nil != err {
		logger.Error().Err(err).Msg("Failed to join album base URL with album id")
		return nil, fmt.Errorf("failed to join album base URL with album id: %v", err)
	}

	reqURL, err := url.Parse(albumURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse album URL")
		return nil, fmt.Errorf("failed to parse album URL: %v", err)
	}

	params := make(url.Values, 1)
	params.Add("countryCode", "US")
	reqURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get album info request")
		return nil, fmt.Errorf("failed to create get album info request: %w", err)
	}

	req.Header.Add("Authorization", "Bearer "+accessToken)
	req.Header.Add("Accept", "application/json")

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetAlbumInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get album info request")
		return nil, fmt.Errorf("failed to send get album info request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get album info response body")
			err = errors.Join(err, fmt.Errorf("failed to close get album info response body: %v", closeErr))
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
		logger.Error().Err(err).Msg("Failed to read album info 200 response body")
		return nil, fmt.Errorf("failed to read album info 200 response body: %w", err)
	}

	var respBody struct {
		Artist struct {
			Name string `json:"name"`
		} `json:"artist"`
		Title        string `json:"title"`
		ReleaseDate  string `json:"releaseDate"`
		CoverID      string `json:"cover"`
		TotalTracks  int    `json:"numberOfTracks"`
		TotalVolumes int    `json:"numberOfVolumes"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode album info response")
		return nil, fmt.Errorf("failed to decode album info response: %w", err)
	}

	releaseDate, err := time.Parse("2006-01-02", respBody.ReleaseDate)
	if nil != err {
		logger.Error().Err(err).Str("release_date", respBody.ReleaseDate).Msg("Failed to parse album release date")
		return nil, fmt.Errorf("failed to parse album release date: %v", err)
	}

	return &types.AlbumMeta{
		Artist:       respBody.Artist.Name,
		Title:        respBody.Title,
		ReleaseDate:  releaseDate,
		CoverID:      respBody.CoverID,
		TotalTracks:  respBody.TotalTracks,
		TotalVolumes: respBody.TotalVolumes,
	}, nil
}
