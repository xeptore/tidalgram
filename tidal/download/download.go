package download

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/errgroup"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/config"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/ptr"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/mpd"
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
	maxBatchParts              = 10
	singlePartChunkSize        = 1024 * 1024
)

var ErrTooManyRequests = errors.New("too many requests")

type Downloader struct {
	dir                   fs.DownloadDir
	auth                  *auth.Auth
	conf                  config.Tidal
	albumsMetaCache       *cache.AlbumsMetaCache
	downloadedCoversCache *cache.DownloadedCoversCache
	trackCreditsCache     *cache.TrackCreditsCache
}

func NewDownloader(
	dir fs.DownloadDir,
	conf config.Tidal,
	auth *auth.Auth,
	albumsMetaCache *cache.AlbumsMetaCache,
	downloadedCoversCache *cache.DownloadedCoversCache,
	trackCreditsCache *cache.TrackCreditsCache,
) *Downloader {
	return &Downloader{
		dir:                   dir,
		conf:                  conf,
		auth:                  auth,
		albumsMetaCache:       albumsMetaCache,
		downloadedCoversCache: downloadedCoversCache,
		trackCreditsCache:     trackCreditsCache,
	}
}

func (d *Downloader) Single(ctx context.Context, id string) (err error) {
	accessToken := d.auth.Credentials().Token

	track, err := getSingleTrackMeta(ctx, accessToken, id)
	if nil != err {
		return err
	}

	trackFs := d.dir.Single(id)
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
				err = errors.Join(err, fmt.Errorf("failed to remove track file: %v", removeErr))
			}
		}
	}()

	format, err := d.downloadTrack(ctx, accessToken, id, trackFs.Path)
	if nil != err {
		return err
	}

	trackCredits, err := d.getTrackCredits(ctx, accessToken, id)
	if nil != err {
		return err
	}

	trackLyrics, err := d.fetchTrackLyrics(ctx, accessToken, id)
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
}

func trackCaption(album types.AlbumMeta) string {
	return fmt.Sprintf("%s (%s)", album.Title, album.ReleaseDate.Format(types.ReleaseDateLayout))
}

func (d *Downloader) getTrackCredits(
	ctx context.Context,
	accessToken, id string,
) (*types.TrackCredits, error) {
	cachedTrackCredits, err := d.trackCreditsCache.Fetch(
		id,
		cache.DefaultTrackCreditsTTL,
		func() (*types.TrackCredits, error) { return d.fetchTrackCredits(ctx, accessToken, id) },
	)
	if nil != err {
		return nil, err
	}
	return cachedTrackCredits.Value(), nil
}

func (d *Downloader) fetchTrackCredits(
	ctx context.Context,
	accessToken string,
	id string,
) (c *types.TrackCredits, err error) {
	reqURL, err := url.Parse(fmt.Sprintf(trackCreditsAPIFormat, id))
	if nil != err {
		return nil, fmt.Errorf("failed to parse track credits URL: %v", err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("countryCode", "US")
	reqParams.Add("includeContributors", "true")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get track credits request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetTrackCredits) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return nil, context.DeadlineExceeded
		default:
			return nil, fmt.Errorf("failed to send get track credits request: %v", err)
		}
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track credits response body: %v", closeErr),
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

		return nil, errors.New("unexpected 403 response")
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

	var respBody TrackCreditsResponse
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode track credits response: %v", err)
	}

	return ptr.Of(respBody.toTrackCredits()), nil
}

func (d *Downloader) fetchTrackLyrics(
	ctx context.Context,
	accessToken string,
	id string,
) (l string, err error) {
	reqURL, err := url.Parse(fmt.Sprintf(trackLyricsAPIFormat, id))
	if nil != err {
		return "", fmt.Errorf("failed to parse track lyrics URL: %v", err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("countryCode", "US")
	reqParams.Add("includeContributors", "true")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return "", fmt.Errorf("failed to create get track lyrics request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetTrackLyrics) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return "", context.DeadlineExceeded
		default:
			return "", fmt.Errorf("failed to send get track lyrics request: %v", err)
		}
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track lyrics response body: %v", closeErr),
			)
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", nil
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return "", err
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return "", err
		} else if ok {
			return "", auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return "", err
		} else if ok {
			return "", auth.ErrUnauthorized
		}

		return "", errors.New("received 401 response")
	case http.StatusTooManyRequests:
		return "", ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return "", err
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return "", err
		} else if ok {
			return "", ErrTooManyRequests
		}

		return "", errors.New("unexpected 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return "", err
		}
		return "", fmt.Errorf("unexpected status code: %d", code)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return "", err
	}

	if !gjson.ValidBytes(respBytes) {
		return "", fmt.Errorf("invalid track lyrics response json: %v", err)
	}

	var lyrics string
	if lyricsKey := gjson.GetBytes(respBytes, "subtitles"); lyricsKey.Type == gjson.String {
		lyrics = lyricsKey.Str
	} else if lyricsKey := gjson.GetBytes(respBytes, "lyrics"); lyricsKey.Type == gjson.String {
		lyrics = lyricsKey.Str
	} else {
		return "", fmt.Errorf("unexpected track lyrics response: %v", err)
	}

	return lyrics, nil
}

type TrackCreditsResponse []struct {
	Type         string `json:"type"`
	Contributors []struct {
		Name string `json:"name"`
		ID   int    `json:"id"`
	} `json:"contributors"`
}

func (t TrackCreditsResponse) toTrackCredits() types.TrackCredits {
	var out types.TrackCredits
	for _, v := range t {
		switch v.Type {
		case "Producer":
			for _, v := range v.Contributors {
				out.Producers = append(out.Producers, v.Name)
			}
		case "Composer":
			for _, v := range v.Contributors {
				out.Composers = append(out.Composers, v.Name)
			}
		case "Lyricist":
			for _, v := range v.Contributors {
				out.Lyricists = append(out.Lyricists, v.Name)
			}
		case "Additional Producer":
			for _, v := range v.Contributors {
				out.AdditionalProducers = append(out.AdditionalProducers, v.Name)
			}
		}
	}
	return out
}

type TrackEmbeddedAttrs struct {
	LeadArtist   string
	Album        string
	AlbumArtist  string
	Artists      []types.TrackArtist
	Copyright    string
	CoverPath    string
	Format       types.TrackFormat
	ISRC         string
	ReleaseDate  time.Time
	Title        string
	TrackNumber  int
	TotalTracks  int
	Version      *string
	VolumeNumber int
	TotalVolumes int
	Credits      types.TrackCredits
	Lyrics       string
}

func embedTrackAttributes(
	ctx context.Context,
	trackFilePath string,
	attrs TrackEmbeddedAttrs,
) (err error) {
	ext := attrs.Format.InferTrackExt()
	trackFilePathWithExt := trackFilePath + "." + ext

	metaTags := []string{
		"artist=" + types.JoinArtists(attrs.Artists),
		"lead_performer=" + attrs.LeadArtist,
		"title=" + attrs.Title,
		"album=" + attrs.Album,
		"album_artist=" + attrs.AlbumArtist,
		"copyright=" + attrs.Copyright,
		"isrc=" + attrs.ISRC,
		"track=" + strconv.Itoa(attrs.TrackNumber),
		"tracktotal=" + strconv.Itoa(attrs.TotalTracks),
		"disc=" + strconv.Itoa(attrs.VolumeNumber),
		"disctotal=" + strconv.Itoa(attrs.TotalVolumes),
		"date=" + attrs.ReleaseDate.Format(time.DateOnly),
		"year=" + strconv.Itoa(attrs.ReleaseDate.Year()),
		"lyrics=" + lo.Ternary(len(attrs.Lyrics) == 0, "", attrs.Lyrics),
	}

	if len(attrs.Credits.Composers) > 0 {
		metaTags = append(metaTags, "composer="+types.JoinNames(attrs.Credits.Composers))
	}
	if len(attrs.Credits.Lyricists) > 0 {
		metaTags = append(metaTags, "lyricist="+types.JoinNames(attrs.Credits.Lyricists))
	}
	if len(attrs.Credits.Producers) > 0 {
		metaTags = append(metaTags, "producer="+types.JoinNames(attrs.Credits.Producers))
	}
	if len(attrs.Credits.AdditionalProducers) > 0 {
		metaTags = append(
			metaTags,
			"coproducer="+types.JoinNames(attrs.Credits.AdditionalProducers),
		)
	}

	if nil != attrs.Version {
		metaTags = append(metaTags, "version="+*attrs.Version)
	}

	metaArgs := make([]string, 0, len(metaTags)*2)
	for _, tag := range metaTags {
		metaArgs = append(metaArgs, "-metadata", tag)
	}

	args := []string{
		"-i",
		trackFilePath,
		"-i",
		attrs.CoverPath,
		"-map",
		"0:a",
		"-map",
		"1",
		"-c",
		"copy",
		"-disposition:v",
		"attached_pic",
	}
	args = append(args, metaArgs...)
	args = append(args, trackFilePathWithExt)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if err := cmd.Run(); nil != err {
		return fmt.Errorf("failed to write track attributes: %v", err)
	}
	if err := os.Rename(trackFilePathWithExt, trackFilePath); nil != err {
		return fmt.Errorf("failed to rename track file: %v", err)
	}
	return nil
}

func getSingleTrackMeta(
	ctx context.Context,
	accessToken, id string,
) (m *SingleTrackMeta, err error) {
	trackURL := fmt.Sprintf(trackAPIFormat, id)

	reqURL, err := url.Parse(trackURL)
	if nil != err {
		return nil, fmt.Errorf("failed to parse track URL: %v", err)
	}

	reqParams := make(url.Values, 1)
	reqParams.Add("countryCode", "US")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get track info request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		return nil, fmt.Errorf("failed to send get track info request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track info response body: %v", closeErr),
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

		return nil, errors.New("unexpected 403 response")
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
		Duration     int    `json:"duration"`
		Title        string `json:"title"`
		TrackNumber  int    `json:"trackNumber"`
		VolumeNumber int    `json:"volumeNumber"`
		Copyright    string `json:"copyright"`
		ISRC         string `json:"isrc"`
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
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode track info response body: %v", err)
	}

	artists := make([]types.TrackArtist, len(respBody.Artists))
	for i, artist := range respBody.Artists {
		switch artist.Type {
		case types.ArtistTypeMain, types.ArtistTypeFeatured:
		default:
			return nil, fmt.Errorf("unexpected artist type: %s", artist.Type)
		}
		artists[i] = types.TrackArtist{Name: artist.Name, Type: artist.Type}
	}

	track := SingleTrackMeta{
		Artist:       respBody.Artist.Name,
		AlbumID:      strconv.Itoa(respBody.Album.ID),
		AlbumTitle:   respBody.Album.Title,
		Artists:      artists,
		ISRC:         respBody.ISRC,
		Copyright:    respBody.Copyright,
		CoverID:      respBody.Album.CoverID,
		Duration:     respBody.Duration,
		Title:        respBody.Title,
		TrackNumber:  respBody.TrackNumber,
		Version:      respBody.Version,
		VolumeNumber: respBody.VolumeNumber,
	}
	return &track, nil
}

func (d *Downloader) getCover(
	ctx context.Context,
	accessToken, coverID string,
) (b []byte, err error) {
	cachedCoverBytes, err := d.downloadedCoversCache.Fetch(
		coverID,
		cache.DefaultDownloadedCoverTTL,
		func() ([]byte, error) { return d.downloadCover(ctx, accessToken, coverID) },
	)
	if nil != err {
		return nil, err
	}
	return cachedCoverBytes.Value(), nil
}

func (d *Downloader) downloadCover(
	ctx context.Context,
	accessToken, coverID string,
) (b []byte, err error) {
	coverURL, err := url.JoinPath(
		fmt.Sprintf(coverURLFormat, strings.ReplaceAll(coverID, "-", "/")),
	)
	if nil != err {
		return nil, fmt.Errorf("failed to join cover base URL with cover filepath: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get cover request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.DownloadTimeouts.DownloadCover) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		return nil, fmt.Errorf("failed to send get track cover request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track cover response body: %v", closeErr),
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

		return nil, errors.New("unexpected 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected status code received from get track cover: %d", code)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, err
	}
	return respBytes, nil
}

func (d *Downloader) getAlbumMeta(
	ctx context.Context,
	accessToken, id string,
) (*types.AlbumMeta, error) {
	cachedAlbumMeta, err := d.albumsMetaCache.Fetch(
		id,
		cache.DefaultAlbumTTL,
		func() (*types.AlbumMeta, error) { return d.fetchAlbumMeta(ctx, accessToken, id) },
	)
	if nil != err {
		return nil, err
	}
	return cachedAlbumMeta.Value(), nil
}

func (d *Downloader) fetchAlbumMeta(
	ctx context.Context,
	accessToken, id string,
) (m *types.AlbumMeta, err error) {
	albumURL, err := url.JoinPath(fmt.Sprintf(albumAPIFormat, id))
	if nil != err {
		return nil, fmt.Errorf("failed to join album base URL with album id: %v", err)
	}

	reqURL, err := url.Parse(albumURL)
	if nil != err {
		return nil, fmt.Errorf("failed to parse album URL: %v", err)
	}

	params := make(url.Values, 1)
	params.Add("countryCode", "US")
	reqURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get album info request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetAlbumInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		return nil, fmt.Errorf("failed to send get album info request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get album info response body: %v", closeErr),
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

		return nil, errors.New("unexpected 403 response")
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
		return nil, fmt.Errorf("failed to decode album info response: %v", err)
	}

	releaseDate, err := time.Parse("2006-01-02", respBody.ReleaseDate)
	if nil != err {
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

func (d *Downloader) downloadTrack(
	ctx context.Context,
	accessToken, id string,
	fileName string,
) (*types.TrackFormat, error) {
	stream, format, err := d.getStream(ctx, accessToken, id)
	if nil != err {
		return nil, err
	}

	waitTime := ratelimit.TrackDownloadSleepMS()
	time.Sleep(waitTime)

	if err := stream.saveTo(ctx, accessToken, fileName); nil != err {
		return nil, err
	}

	return format, nil
}

type Stream interface {
	saveTo(ctx context.Context, accessToken string, fileName string) error
}

func (d *Downloader) getStream(
	ctx context.Context,
	accessToken, id string,
) (s Stream, f *types.TrackFormat, err error) {
	trackURL := fmt.Sprintf(trackStreamAPIFormat, id)

	reqURL, err := url.Parse(trackURL)
	if nil != err {
		return nil, nil, fmt.Errorf("failed to parse track URL to build track stream URLs: %v", err)
	}

	params := make(url.Values, 6)
	params.Add("countryCode", "US")
	params.Add("audioquality", "HI_RES_LOSSLESS")
	params.Add("playbackmode", "STREAM")
	params.Add("assetpresentation", "FULL")
	params.Add("immersiveaudio", "false")
	params.Add("locale", "en")

	reqURL.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, nil, fmt.Errorf("failed to create get track stream URLs request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetStreamURLs) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		return nil, nil, fmt.Errorf("failed to send get track stream URLs request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get track stream URLs response body: %v", closeErr),
			)
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, nil, err
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, nil, err
		} else if ok {
			return nil, nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, nil, err
		} else if ok {
			return nil, nil, auth.ErrUnauthorized
		}

		return nil, nil, errors.New("received 401 response")
	case http.StatusTooManyRequests:
		return nil, nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, nil, err
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return nil, nil, err
		} else if ok {
			return nil, nil, ErrTooManyRequests
		}

		return nil, nil, errors.New("unexpected 403 response")
	default:
		_, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf(
			"unexpected status code received from get track stream URLs: %d",
			code,
		)
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, nil, err
	}
	var respBody struct {
		ManifestMimeType string `json:"manifestMimeType"`
		Manifest         string `json:"manifest"`
	}
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, nil, fmt.Errorf("failed to decode track stream response body: %v", err)
	}

	switch mimeType := respBody.ManifestMimeType; mimeType {
	case "application/dash+xml", "dash+xml":
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Manifest))
		info, err := mpd.ParseStreamInfo(dec)
		if nil != err {
			return nil, nil, fmt.Errorf("failed to parse stream info: %v", err)
		}

		if _, err := types.InferTrackExt(info.MimeType, info.Codec); nil != err {
			return nil, nil, err
		}
		format := types.TrackFormat{MimeType: info.MimeType, Codec: info.Codec}

		return &DashTrackStream{
			Info: *info,
			DownloadTimeout: time.Duration(
				d.conf.DownloadTimeouts.DownloadDashSegment,
			) * time.Second,
		}, &format, nil
	case "application/vnd.tidal.bts", "vnd.tidal.bt":
		var manifest VNDManifest
		dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(respBody.Manifest))
		if err := json.NewDecoder(dec).Decode(&manifest); nil != err {
			return nil, nil, fmt.Errorf("failed to decode vnd.tidal.bt manifest: %v", err)
		}

		switch manifest.EncryptionType {
		case "NONE":
		default:
			return nil, nil, fmt.Errorf(
				"encrypted vnd.tidal.bt manifest is not yet implemented: %s",
				manifest.EncryptionType,
			)
		}

		if _, err := types.InferTrackExt(manifest.MimeType, manifest.Codec); nil != err {
			return nil, nil, err
		}
		format := &types.TrackFormat{MimeType: manifest.MimeType, Codec: manifest.Codec}

		if len(manifest.URLs) == 0 {
			return nil, nil, errors.New("empty vnd.tidal.bt manifest URLs")
		}
		return &VndTrackStream{
			URL: manifest.URLs[0],
			DownloadTimeout: time.Duration(
				d.conf.DownloadTimeouts.DownloadVNDSegment,
			) * time.Second,
			GetTrackFileSizeTimeout: time.Duration(
				d.conf.DownloadTimeouts.GetVNDTrackFileSize,
			) * time.Second,
		}, format, nil
	default:
		return nil, nil, fmt.Errorf("unexpected manifest mime type: %s", mimeType)
	}
}

type SingleTrackMeta struct {
	Artist       string
	AlbumID      string
	AlbumTitle   string
	Artists      []types.TrackArtist
	ISRC         string
	Copyright    string
	CoverID      string
	Duration     int
	Title        string
	TrackNumber  int
	Version      *string
	VolumeNumber int
}

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

func (d *Downloader) Playlist(ctx context.Context, id string) error {
	accessToken := d.auth.Credentials().Token

	playlist, err := d.getPlaylistMeta(ctx, accessToken, id)
	if nil != err {
		return err
	}

	tracks, err := d.getPlaylistTracks(ctx, accessToken, id)
	if nil != err {
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

			trackLyrics, err := d.fetchTrackLyrics(ctx, accessToken, track.ID)
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

func (d *Downloader) getPlaylistMeta(
	ctx context.Context,
	accessToken, id string,
) (m *PlaylistMeta, err error) {
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
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetPlaylistInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
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

func (d *Downloader) getPlaylistTracks(
	ctx context.Context,
	accessToken, id string,
) ([]ListTrackMeta, error) {
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

func (d *Downloader) playlistTracksPage(
	ctx context.Context,
	accessToken, id string,
	page int,
) (ts []ListTrackMeta, rem int, err error) {
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

func (d *Downloader) Mix(ctx context.Context, id string) error {
	accessToken := d.auth.Credentials().Token

	mix, err := d.getMixMeta(ctx, accessToken, id)
	if nil != err {
		return err
	}

	tracks, err := d.getMixTracks(ctx, accessToken, id)
	if nil != err {
		return err
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
					if removeErr := trackFs.Remove(); nil != removeErr &&
						!errors.Is(err, os.ErrNotExist) {
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

			trackLyrics, err := d.fetchTrackLyrics(ctx, accessToken, track.ID)
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

	info := types.StoredMix{
		Caption:  mix.Title,
		TrackIDs: lo.Map(tracks, func(t ListTrackMeta, _ int) string { return t.ID }),
	}
	if err := mixFs.InfoFile.Write(info); nil != err {
		return err
	}

	return nil
}

func (d *Downloader) getMixMeta(
	ctx context.Context,
	accessToken, id string,
) (m *MixMeta, err error) {
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
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetMixInfo) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
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

		return nil, errors.New("unexpected 403 response")
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

func (d *Downloader) getMixTracks(
	ctx context.Context,
	accessToken, id string,
) ([]ListTrackMeta, error) {
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

func (d *Downloader) mixTracksPage(
	ctx context.Context,
	accessToken, id string,
	page int,
) (ts []ListTrackMeta, rem int, err error) {
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

func (d *Downloader) Album(ctx context.Context, id string) error {
	accessToken := d.auth.Credentials().Token

	album, err := d.getAlbumMeta(ctx, accessToken, id)
	if nil != err {
		return err
	}

	albumFs := d.dir.Album(id)
	if exists, err := albumFs.Cover.Exists(); nil != err {
		return err
	} else if !exists {
		coverBytes, err := d.getCover(ctx, accessToken, album.CoverID)
		if nil != err {
			return err
		}
		if err := albumFs.Cover.Write(coverBytes); nil != err {
			return err
		}
	}

	volumes, err := d.getAlbumVolumes(ctx, accessToken, id)
	if nil != err {
		return err
	}

	for _, volTracks := range volumes {
		for _, track := range volTracks {
			d.trackCreditsCache.Set(track.ID, &track.Credits, cache.DefaultTrackCreditsTTL)
		}
	}

	var (
		wg, wgCtx           = errgroup.WithContext(ctx)
		albumVolumeTrackIDs = make([][]string, len(volumes))
	)

	wg.SetLimit(ratelimit.AlbumDownloadConcurrency)
	for i, tracks := range volumes {
		albumVolumeTrackIDs[i] = lo.Map(
			tracks,
			func(t AlbumTrackMeta, _ int) string { return t.ID },
		)

		volNum := i + 1
		for _, track := range tracks {
			wg.Go(func() (err error) {
				trackFs := albumFs.Track(volNum, track.ID)
				if exists, err := trackFs.Exists(); nil != err {
					return err
				} else if exists {
					return nil
				}
				defer func() {
					if nil != err {
						if removeErr := trackFs.Remove(); nil != removeErr {
							err = fmt.Errorf("failed to remove track file: %v: %w", removeErr, err)
						}
					}
				}()

				trackLyrics, err := d.fetchTrackLyrics(ctx, accessToken, id)
				if nil != err {
					return err
				}

				format, err := d.downloadTrack(wgCtx, accessToken, track.ID, trackFs.Path)
				if nil != err {
					return err
				}

				attrs := TrackEmbeddedAttrs{
					LeadArtist:   track.Artist,
					Album:        album.Title,
					AlbumArtist:  album.Artist,
					Artists:      track.Artists,
					Copyright:    track.Copyright,
					CoverPath:    albumFs.Cover.Path,
					Format:       *format,
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
						CoverID:  album.CoverID,
					},
					Caption: trackCaption(*album),
				}
				if err := trackFs.InfoFile.Write(info); nil != err {
					return err
				}

				return nil
			})
		}
	}

	if err := wg.Wait(); nil != err {
		return err
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
		return err
	}

	return nil
}

func (d *Downloader) getAlbumVolumes(
	ctx context.Context,
	accessToken, id string,
) ([][]AlbumTrackMeta, error) {
	var (
		tracks              [][]AlbumTrackMeta
		currentVolumeTracks []AlbumTrackMeta
		currentVolume       = 1
	)

	for i := 0; ; i++ {
		pageTracks, rem, err := d.albumTracksPage(ctx, accessToken, id, i)
		if nil != err {
			return nil, err
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
	accessToken, id string,
	page int,
) (ts []AlbumTrackMeta, rem int, err error) {
	albumURL, err := url.JoinPath(fmt.Sprintf(albumItemsCreditsAPIFormat, id))
	if nil != err {
		return nil, 0, fmt.Errorf("failed to join album tracks credits URL with id: %v", err)
	}

	respBytes, err := d.getAlbumPagedItems(ctx, accessToken, albumURL, page)
	if nil != err {
		return nil, 0, err
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
		return nil, 0, fmt.Errorf("failed to decode album items page response: %v", err)
	}

	thisPageItemsCount := len(respBody.Items)
	if thisPageItemsCount == 0 {
		return nil, 0, os.ErrNotExist
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
	accessToken, itemsURL string,
	page int,
) ([]byte, error) {
	reqParams := make(url.Values, 3)
	reqParams.Add("countryCode", "US")
	reqParams.Add("limit", strconv.Itoa(pageSize))
	reqParams.Add("offset", strconv.Itoa(page*pageSize))
	return d.getPagedItems(ctx, accessToken, itemsURL, reqParams)
}

func (d *Downloader) getListPagedItems(
	ctx context.Context,
	accessToken, itemsURL string,
	page int,
) ([]byte, error) {
	reqParams := make(url.Values, 3)
	reqParams.Add("countryCode", "US")
	reqParams.Add("limit", strconv.Itoa(pageSize))
	reqParams.Add("offset", strconv.Itoa(page*pageSize))
	return d.getPagedItems(ctx, accessToken, itemsURL, reqParams)
}

func (d *Downloader) getPagedItems(
	ctx context.Context,
	accessToken, itemsURL string,
	reqParams url.Values,
) (b []byte, err error) {
	reqURL, err := url.Parse(itemsURL)
	if nil != err {
		return nil, fmt.Errorf("failed to parse page items URL: %v", err)
	}

	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get page items request: %v", err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.DownloadTimeouts.GetPagedTracks) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		return nil, fmt.Errorf("failed to send get page items request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(
				err,
				fmt.Errorf("failed to close get page items response body: %v", closeErr),
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

		return nil, errors.New("unexpected 403 response")
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
	return respBytes, nil
}
