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
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/types"
)

func (d *Downloader) mix(ctx context.Context, id string) error {
	accessToken := d.auth.Credentials().Token

	mix, err := d.getMixMeta(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

		if errors.Is(err, auth.ErrUnauthorized) {
			return auth.ErrUnauthorized
		}

		if errors.Is(err, ErrTooManyRequests) {
			return ErrTooManyRequests
		}

		return fmt.Errorf("failed to get mix meta: %v", err)
	}

	tracks, err := d.getMixTracks(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

		if errors.Is(err, auth.ErrUnauthorized) {
			return auth.ErrUnauthorized
		}

		if errors.Is(err, ErrTooManyRequests) {
			return ErrTooManyRequests
		}

		return fmt.Errorf("failed to get mix tracks: %v", err)
	}

	var (
		mixFs     = d.dir.Mix(id)
		wg, wgCtx = errgroup.WithContext(ctx)
	)

	wg.SetLimit(ratelimit.MixDownloadConcurrency)
	for _, track := range tracks {
		wg.Go(func() (err error) {
			trackFs := mixFs.Track(track.ID)
			if exists, err := trackFs.Cover.Exists(); nil != err {
				return err
			} else if !exists {
				coverBytes, err := d.getCover(ctx, accessToken, track.CoverID)
				if nil != err {
					if errors.Is(err, context.DeadlineExceeded) {
						return context.DeadlineExceeded
					}

					if errors.Is(err, context.Canceled) {
						return context.Canceled
					}

					if errors.Is(err, auth.ErrUnauthorized) {
						return auth.ErrUnauthorized
					}

					if errors.Is(err, ErrTooManyRequests) {
						return ErrTooManyRequests
					}

					return fmt.Errorf("failed to get track cover: %v", err)
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
						if !errors.Is(err, os.ErrNotExist) {
							err = errors.Join(err, fmt.Errorf("failed to remove mix track file: %v", removeErr))
						}
					}
				}
			}()

			trackCredits, err := d.getTrackCredits(ctx, accessToken, track.ID)
			if nil != err {
				if errors.Is(err, context.DeadlineExceeded) {
					return context.DeadlineExceeded
				}

				if errors.Is(err, context.Canceled) {
					return context.Canceled
				}

				if errors.Is(err, auth.ErrUnauthorized) {
					return auth.ErrUnauthorized
				}

				if errors.Is(err, ErrTooManyRequests) {
					return ErrTooManyRequests
				}

				return fmt.Errorf("failed to get track credits: %v", err)
			}

			trackLyrics, err := d.downloadTrackLyrics(ctx, accessToken, track.ID)
			if nil != err {
				if errors.Is(err, context.DeadlineExceeded) {
					return context.DeadlineExceeded
				}

				if errors.Is(err, context.Canceled) {
					return context.Canceled
				}

				if errors.Is(err, auth.ErrUnauthorized) {
					return auth.ErrUnauthorized
				}

				if errors.Is(err, ErrTooManyRequests) {
					return ErrTooManyRequests
				}

				return fmt.Errorf("failed to download track lyrics: %v", err)
			}

			format, err := d.downloadTrack(wgCtx, accessToken, track.ID, trackFs.Path)
			if nil != err {
				if errors.Is(err, context.DeadlineExceeded) {
					return context.DeadlineExceeded
				}

				if errors.Is(err, context.Canceled) {
					return context.Canceled
				}

				if errors.Is(err, auth.ErrUnauthorized) {
					return auth.ErrUnauthorized
				}

				if errors.Is(err, ErrTooManyRequests) {
					return ErrTooManyRequests
				}

				return fmt.Errorf("failed to download track: %v", err)
			}

			album, err := d.getAlbumMeta(ctx, accessToken, track.AlbumID)
			if nil != err {
				if errors.Is(err, context.DeadlineExceeded) {
					return context.DeadlineExceeded
				}

				if errors.Is(err, context.Canceled) {
					return context.Canceled
				}

				if errors.Is(err, auth.ErrUnauthorized) {
					return auth.ErrUnauthorized
				}

				if errors.Is(err, ErrTooManyRequests) {
					return ErrTooManyRequests
				}

				return fmt.Errorf("failed to get album meta: %v", err)
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

	info := types.StoredMix{
		Caption:  mix.Title,
		TrackIDs: lo.Map(tracks, func(t ListTrackMeta, _ int) string { return t.ID }),
	}
	if err := mixFs.InfoFile.Write(info); nil != err {
		return err
	}

	return nil
}

func (d *Downloader) getMixMeta(ctx context.Context, accessToken, id string) (m *MixMeta, err error) {
	reqURL, err := url.Parse(mixInfoURL)
	if nil != err {
		return nil, fmt.Errorf("failed to parse playlist URL: %v", err)
	}

	reqParams := make(url.Values, 4)
	reqParams.Add("mixId", id)
	reqParams.Add("countryCode", "US")
	reqParams.Add("locale", "en_US")
	reqParams.Add("deviceType", "BROWSER")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get mix info request: %v", err)
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
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}

		return nil, fmt.Errorf("failed to send get mix info request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get mix info response body: %v", closeErr),
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

		return nil, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
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

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, err
		}

		return nil, fmt.Errorf("unexpected response code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, err
	}

	if !gjson.ValidBytes(respBytes) {
		return nil, fmt.Errorf("invalid mix info response json: %v", err)
	}

	var title string
	switch titleKey := gjson.GetBytes(respBytes, "title"); titleKey.Type { //nolint:exhaustive
	case gjson.String:
		title = titleKey.Str
	default:
		return nil, fmt.Errorf("unexpected mix info response: %v", err)
	}

	return &MixMeta{Title: title}, nil
}

type MixMeta struct {
	Title string
}

func (d *Downloader) getMixTracks(ctx context.Context, accessToken, id string) ([]ListTrackMeta, error) {
	var tracks []ListTrackMeta

	for i := 0; ; i++ {
		pageTracks, rem, err := d.mixTracksPage(ctx, accessToken, id, i)
		if nil != err {
			switch {
			case errors.Is(err, os.ErrNotExist):
				break
			default:
				return nil, err
			}
		}
		tracks = append(tracks, pageTracks...)

		if rem == 0 {
			break
		}
	}

	return tracks, nil
}

func (d *Downloader) mixTracksPage(ctx context.Context, accessToken, id string, page int) (ts []ListTrackMeta, rem int, err error) {
	mixURL, err := url.JoinPath(fmt.Sprintf(mixItemsAPIFormat, id))
	if nil != err {
		return nil, 0, fmt.Errorf("failed to create mix URL: %v", err)
	}

	respBytes, err := d.getListPagedItems(ctx, accessToken, mixURL, page)
	if nil != err {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, 0, nil
		default:
			return nil, 0, err
		}
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
