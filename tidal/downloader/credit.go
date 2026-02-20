package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/tidal/types"
)

func (d *Downloader) artistCredits(ctx context.Context, logger zerolog.Logger, id string) error {
	creds := d.auth.Credentials()

	tracks, err := d.getArtistCreditsTracks(ctx, logger, creds.Token, creds.CountryCode, id)
	if nil != err {
		return fmt.Errorf("get artist credits tracks: %w", err)
	}

	var (
		creditsFs = d.dir.ArtistCredits(id)
		wg, wgctx = errgroup.WithContext(ctx)
	)

	wg.SetLimit(d.conf.Concurrency.ArtistCreditsTracks)

	for i, track := range tracks {
		wg.Go(func() (err error) {
			select {
			case <-wgctx.Done():
				return nil
			default:
			}

			logger := logger.With().Int("track_index", i).Str("track_id", track.ID).Logger()

			trackFs := creditsFs.Track(track.ID)

			if exists, err := trackFs.Cover.AlreadyDownloaded(); nil != err {
				logger.Error().Err(err).Msg("Failed to check if track cover exists")
				return fmt.Errorf("check if track cover exists: %v", err)
			} else if !exists {
				coverBytes, err := d.getCover(wgctx, logger, creds.Token, track.CoverID)
				if nil != err {
					return fmt.Errorf("get track cover: %w", err)
				}

				if err := trackFs.Cover.Write(coverBytes); nil != err {
					logger.Error().Err(err).Msg("Failed to write track cover")
					return fmt.Errorf("write track cover: %v", err)
				}
			}

			if exists, err := trackFs.AlreadyDownloaded(); nil != err {
				logger.Error().Err(err).Msg("Failed to check if track exists")
				return fmt.Errorf("check if track exists: %v", err)
			} else if exists {
				return nil
			}
			defer func() {
				if nil != err {
					if removeErr := trackFs.Remove(); nil != removeErr {
						if !errors.Is(removeErr, os.ErrNotExist) {
							logger.Error().Err(removeErr).Msg("Failed to remove artist credits track file")
							err = errors.Join(err, fmt.Errorf("remove artist credits track file: %v", removeErr))
						}
					}
				}
			}()

			ext, err := d.downloadTrack(wgctx, logger, creds.Token, track.ID, trackFs.Path)
			if nil != err {
				return fmt.Errorf("download track: %w", err)
			}

			trackCredits, err := d.getTrackCredits(wgctx, logger, creds.Token, creds.CountryCode, track.ID)
			if nil != err {
				return fmt.Errorf("get track credits: %w", err)
			}

			trackLyrics, err := d.downloadTrackLyrics(wgctx, logger, creds.Token, creds.CountryCode, track.ID)
			if nil != err {
				return fmt.Errorf("download track lyrics: %w", err)
			}

			album, err := d.getAlbumMeta(wgctx, logger, creds.Token, creds.CountryCode, track.AlbumID)
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
				return fmt.Errorf("embed track attributes: %w", err)
			}

			info := types.StoredTrack{
				Track: types.Track{
					Artists:      track.Artists,
					Title:        track.Title,
					TrackNumber:  track.TrackNumber,
					VolumeNumber: track.VolumeNumber,
					Duration:     track.Duration,
					Version:      track.Version,
					CoverID:      track.CoverID,
					Ext:          ext,
				},
				Caption: trackCaption(album.Title, album.ReleaseDate),
			}
			if err := trackFs.InfoFile.Write(info); nil != err {
				logger.Error().Err(err).Msg("Failed to write track info")
				return fmt.Errorf("write track info: %v", err)
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return fmt.Errorf("wait for track download workers: %w", err)
	}

	info := types.StoredArtistCredits{
		TrackIDs: lo.Map(tracks, func(t ListTrackMeta, _ int) string { return t.ID }),
	}
	if err := creditsFs.InfoFile.Write(info); nil != err {
		logger.Error().Err(err).Msg("Failed to write artist credits info")
		return fmt.Errorf("write artist credits info: %v", err)
	}

	return nil
}

func (d *Downloader) getArtistCreditsTracks(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	countryCode string,
	id string,
) ([]ListTrackMeta, error) {
	var tracks []ListTrackMeta

	pagePath, err := d.getArtistCreditsPagePath(ctx, logger, accessToken, countryCode, id)
	if nil != err {
		return nil, fmt.Errorf("get artist credits page path: %w", err)
	}
	if pagePath == "" {
		return nil, errors.New("artist credits page path is empty")
	}

	for i := 0; ; i++ {
		pageTracks, rem, err := d.artistCreditsTracksPage(ctx, logger, accessToken, countryCode, pagePath, id, i)
		if nil != err {
			return nil, fmt.Errorf("get artist credits tracks page: %w", err)
		}

		tracks = append(tracks, pageTracks...)

		if rem == 0 {
			break
		}
	}

	return tracks, nil
}

func (d *Downloader) getArtistCreditsPagePath(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken,
	countryCode,
	id string,
) (string, error) {
	reqURL, err := url.Parse("https://tidal.com/v1/pages/contributor")
	if nil != err {
		return "", fmt.Errorf("parse artist credits page URL: %w", err)
	}
	queryParams := url.Values{
		"artistId":    []string{id},
		"countryCode": []string{countryCode},
		"deviceType":  []string{"BROWSER"},
		"locale":      []string{"en_US"},
	}
	reqURL.RawQuery = queryParams.Encode()

	respBytes, err := d.httpGet(ctx, logger, accessToken, reqURL.String())
	if nil != err {
		return "", fmt.Errorf("get artist credits page: %w", err)
	}

	value := gjson.GetBytes(respBytes, "rows.1.modules.0.pagedList.dataApiPath")
	if !value.Exists() {
		return "", errors.New("artist credits page response does not contain data API path")
	}
	if value.Type != gjson.String {
		return "", errors.New("unexpected artist credits page response: data API path is not a string")
	}

	parsedPath, err := url.Parse(value.Str)
	if nil != err {
		return "", fmt.Errorf("parse artist credits page data API path: %w", err)
	}

	return parsedPath.Path, nil
}

func (d *Downloader) artistCreditsTracksPage(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	countryCode string,
	pagePath string,
	id string,
	page int,
) (ts []ListTrackMeta, rem int, err error) {
	urlPath, err := url.JoinPath("v1", pagePath)
	if nil != err {
		return nil, 0, fmt.Errorf("join artist credits tracks page URL path: %w", err)
	}

	artistCreditsURL := url.URL{ //nolint:exhaustruct
		Scheme: "https",
		Host:   "api.tidal.com",
		Path:   urlPath,
	}
	queryParams := url.Values{
		"artistId":    []string{id},
		"offset":      []string{strconv.Itoa(page * artistCreditsPageSize)},
		"limit":       []string{strconv.Itoa(artistCreditsPageSize)},
		"countryCode": []string{countryCode},
		"deviceType":  []string{"BROWSER"},
		"locale":      []string{"en_US"},
	}
	artistCreditsURL.RawQuery = queryParams.Encode()

	respBytes, err := d.httpGet(ctx, logger, accessToken, artistCreditsURL.String())
	if nil != err {
		return nil, 0, fmt.Errorf("get artist credits tracks page: %w", err)
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
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode artist credits tracks page response")
		return nil, 0, fmt.Errorf("decode artist credits tracks page response: %v", err)
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
				logger.Error().Str("artist_type", a.Type).Msg("Unexpected artist credits artist type")
				return nil, 0, fmt.Errorf("unexpected artist credits artist type: %s", a.Type)
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

	rem = max(respBody.TotalNumberOfItems-(thisPageItemsCount+page*artistCreditsPageSize), 0)

	return ts, rem, nil
}
