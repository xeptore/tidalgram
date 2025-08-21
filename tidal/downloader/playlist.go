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
	"github.com/samber/lo"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/types"
)

func (d *Downloader) playlist(ctx context.Context, id string) error {
	accessToken := d.auth.Credentials().Token

	playlist, err := d.getPlaylistMeta(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		return err
	}

	tracks, err := d.getPlaylistTracks(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		return err
	}

	var (
		playlistFs = d.dir.Playlist(id)
		wg, wgCtx  = errgroup.WithContext(ctx)
	)

	wg.SetLimit(ratelimit.PlaylistDownloadConcurrency)
	for _, track := range tracks {
		wg.Go(func() (err error) {
			trackFs := playlistFs.Track(track.ID)
			if exists, err := trackFs.Cover.Exists(); nil != err {
				return err
			} else if !exists {
				coverBytes, err := d.getCover(ctx, accessToken, track.CoverID)
				if nil != err {
					return err
				}
				if err := trackFs.Cover.Write(coverBytes); nil != err {
					return err
				}
			}

			if exists, err := trackFs.Exists(); nil != err {
				return err
			} else if exists {
				return nil
			}
			defer func() {
				if nil != err {
					if removeErr := trackFs.Remove(); nil != removeErr {
						err = errors.Join(
							err,
							fmt.Errorf("failed to remove track file: %v", removeErr),
						)
					}
				}
			}()

			trackCredits, err := d.getTrackCredits(ctx, accessToken, track.ID)
			if nil != err {
				return err
			}

			trackLyrics, err := d.downloadTrackLyrics(ctx, accessToken, track.ID)
			if nil != err {
				return err
			}

			format, err := d.downloadTrack(wgCtx, accessToken, track.ID, trackFs.Path)
			if nil != err {
				return err
			}

			album, err := d.getAlbumMeta(ctx, accessToken, track.AlbumID)
			if nil != err {
				return err
			}

			attrs := TrackEmbeddedAttrs{
				LeadArtist:   track.Artist,
				Album:        track.AlbumTitle,
				AlbumArtist:  album.Artist,
				Artists:      track.Artists,
				Copyright:    track.Copyright,
				CoverPath:    trackFs.Cover.Path,
				Format:       *format,
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
			}
			if err := embedTrackAttributes(ctx, trackFs.Path, attrs); nil != err {
				return err
			}

			info := types.StoredSingleTrack{
				TrackInfo: types.TrackInfo{
					Artists:  track.Artists,
					Title:    track.Title,
					Duration: track.Duration,
					Version:  track.Version,
					Format:   *format,
					CoverID:  track.CoverID,
				},
				Caption: trackCaption(*album),
			}
			if err := trackFs.InfoFile.Write(info); nil != err {
				return err
			}

			return nil
		})
	}

	if err := wg.Wait(); nil != err {
		return err
	}

	info := types.StoredPlaylist{
		Caption:  fmt.Sprintf("%s (%d - %d)", playlist.Title, playlist.StartYear, playlist.EndYear),
		TrackIDs: lo.Map(tracks, func(t ListTrackMeta, _ int) string { return t.ID }),
	}
	if err := playlistFs.InfoFile.Write(info); nil != err {
		return err
	}

	return nil
}

func (d *Downloader) getPlaylistMeta(ctx context.Context, accessToken, id string) (m *PlaylistMeta, err error) {
	playlistURL, err := url.JoinPath(fmt.Sprintf(playlistAPIFormat, id))
	if nil != err {
		return nil, fmt.Errorf("failed to join playlist base URL with playlist id: %v", err)
	}

	reqURL, err := url.Parse(playlistURL)
	if nil != err {
		return nil, fmt.Errorf("failed to parse playlist URL: %v", err)
	}

	queryParams := make(url.Values, 1)
	queryParams.Add("countryCode", "US")
	reqURL.RawQuery = queryParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get playlist info request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetPlaylistInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		return nil, fmt.Errorf("failed to send get playlist info request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get playlist info response body: %v", closeErr),
			)
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, err
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, err
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, err
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		return nil, errors.New("received 401 response")
	case http.StatusTooManyRequests:
		return nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, err
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return nil, err
		} else if ok {
			return nil, ErrTooManyRequests
		}

		return nil, errors.New("received 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, err
		}

		return nil, fmt.Errorf("unexpected status code: %d", code)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, err
	}
	var respBody struct {
		Title       string `json:"title"`
		Created     string `json:"created"`
		LastUpdated string `json:"lastUpdated"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode playlist response: %v", err)
	}

	const dateLayout = "2006-01-02T15:04:05.000-0700"
	createdAt, err := time.Parse(dateLayout, respBody.Created)
	if nil != err {
		return nil, fmt.Errorf("failed to parse playlist created date: %v", err)
	}

	lastUpdatedAt, err := time.Parse(dateLayout, respBody.LastUpdated)
	if nil != err {
		return nil, fmt.Errorf("failed to parse playlist last updated date: %v", err)
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

func (d *Downloader) getPlaylistTracks(ctx context.Context, accessToken, id string) ([]ListTrackMeta, error) {
	var tracks []ListTrackMeta
	for i := 0; ; i++ {
		pageTracks, rem, err := d.playlistTracksPage(ctx, accessToken, id, i)
		if nil != err {
			return nil, err
		}

		tracks = append(tracks, pageTracks...)

		if rem == 0 {
			break
		}
	}

	return tracks, nil
}

const pageItemTypeTrack = "track"

func (d *Downloader) playlistTracksPage(ctx context.Context, accessToken, id string, page int) (ts []ListTrackMeta, rem int, err error) {
	playlistURL, err := url.JoinPath(fmt.Sprintf(playlistItemsAPIFormat, id))
	if nil != err {
		return nil, 0, fmt.Errorf("failed to create playlist URL: %v", err)
	}

	respBytes, err := d.getListPagedItems(ctx, accessToken, playlistURL, page)
	if nil != err {
		return nil, 0, err
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
		return nil, 0, fmt.Errorf("failed to decode playlist response: %v", err)
	}

	thisPageItemsCount := len(respBody.Items)
	if thisPageItemsCount == 0 {
		return nil, 0, os.ErrNotExist
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
				return nil, 0, fmt.Errorf("unexpected artist type: %s", a.Type)
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
