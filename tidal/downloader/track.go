package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/goccy/go-json"
	"github.com/samber/lo"
	"github.com/tidwall/gjson"

	"github.com/xeptore/tidalgram/cache"
	"github.com/xeptore/tidalgram/httputil"
	"github.com/xeptore/tidalgram/must"
	"github.com/xeptore/tidalgram/ptr"
	"github.com/xeptore/tidalgram/ratelimit"
	"github.com/xeptore/tidalgram/tidal/auth"
	"github.com/xeptore/tidalgram/tidal/types"
)

type TrackMeta struct {
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

func (d *Downloader) track(ctx context.Context, id string) (err error) {
	accessToken := d.auth.Credentials().Token

	track, err := getTrackMeta(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, auth.ErrUnauthorized) {
			return auth.ErrUnauthorized
		}

		if errors.Is(err, ErrTooManyRequests) {
			return ErrTooManyRequests
		}

		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

		return fmt.Errorf("failed to get track meta: %v", err)
	}

	trackFs := d.dir.Track(id)
	if exists, err := trackFs.Cover.Exists(); nil != err {
		return fmt.Errorf("failed to check if track cover exists: %v", err)
	} else if !exists {
		coverBytes, err := d.getCover(ctx, accessToken, track.CoverID)
		if nil != err {
			if errors.Is(err, auth.ErrUnauthorized) {
				return auth.ErrUnauthorized
			}

			if errors.Is(err, ErrTooManyRequests) {
				return ErrTooManyRequests
			}

			if errors.Is(err, context.DeadlineExceeded) {
				return context.DeadlineExceeded
			}

			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}

			return fmt.Errorf("failed to get track cover: %v", err)
		}
		if err := trackFs.Cover.Write(coverBytes); nil != err {
			return fmt.Errorf("failed to write track cover: %v", err)
		}
	}

	if exists, err := trackFs.Exists(); nil != err {
		return fmt.Errorf("failed to check if track exists: %v", err)
	} else if exists {
		return nil
	}
	defer func() {
		if nil != err {
			if removeErr := trackFs.Remove(); nil != removeErr {
				if !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("failed to remove track file: %v", removeErr))
				}
			}
		}
	}()

	format, err := d.downloadTrack(ctx, accessToken, id, trackFs.Path)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

		return err
	}

	trackCredits, err := d.getTrackCredits(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

		return err
	}

	trackLyrics, err := d.downloadTrackLyrics(ctx, accessToken, id)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

		return err
	}

	album, err := d.getAlbumMeta(ctx, accessToken, track.AlbumID)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}

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

func getTrackMeta(ctx context.Context, accessToken, id string) (m *TrackMeta, err error) {
	trackURL := fmt.Sprintf(trackAPIFormat, id)
	reqURL, err := url.Parse(trackURL)
	must.NilErr(err)

	reqParams := make(url.Values, 1)
	reqParams.Add("countryCode", "US")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	must.NilErr(err)

	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}

		return nil, fmt.Errorf("failed to send get track info request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = fmt.Errorf("failed to close get track info response body: %v", closeErr)
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 401 response body: %v", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		return nil, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 403 response body: %v", err)
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, ErrTooManyRequests
		}

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read response body: %v", err)
		}

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read 200 response body: %v", err)
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
		return nil, fmt.Errorf("failed to decode track info 200 response body: %v", err)
	}

	artists := make([]types.TrackArtist, len(respBody.Artists))
	for i, artist := range respBody.Artists {
		switch artist.Type {
		case types.ArtistTypeMain, types.ArtistTypeFeatured:
		default:
			return nil, fmt.Errorf("unexpected artist type: %s, expected: %s or %s", artist.Type, types.ArtistTypeMain, types.ArtistTypeFeatured)
		}
		artists[i] = types.TrackArtist{Name: artist.Name, Type: artist.Type}
	}

	track := TrackMeta{
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

func (d *Downloader) downloadTrack(ctx context.Context, accessToken, id string, fileName string) (*types.TrackFormat, error) {
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

func trackCaption(album types.AlbumMeta) string {
	return fmt.Sprintf("%s (%s)", album.Title, album.ReleaseDate.Format(types.ReleaseDateLayout))
}

func (d *Downloader) getTrackCredits(
	ctx context.Context,
	accessToken, id string,
) (*types.TrackCredits, error) {
	cachedTrackCredits, err := d.cache.TrackCredits.Fetch(
		id,
		cache.DefaultTrackCreditsTTL,
		func() (*types.TrackCredits, error) { return d.downloadTrackCredits(ctx, accessToken, id) },
	)
	if nil != err {
		return nil, err
	}

	return cachedTrackCredits.Value(), nil
}

func (d *Downloader) downloadTrackCredits(ctx context.Context, accessToken string, id string) (c *types.TrackCredits, err error) {
	trackCreditsURL := fmt.Sprintf(trackCreditsAPIFormat, id)
	reqURL, err := url.Parse(trackCreditsURL)
	if nil != err {
		return nil, fmt.Errorf("failed to parse track credits URL %s: %v", trackCreditsURL, err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("countryCode", "US")
	reqParams.Add("includeContributors", "true")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return nil, fmt.Errorf("failed to create get track credits request %s: %v", reqURL.String(), err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetTrackCredits) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return nil, context.Canceled
		}

		return nil, fmt.Errorf("failed to send get track credits request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close get track credits response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 401 response body: %v", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return nil, auth.ErrUnauthorized
		}

		return nil, fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return nil, ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read 403 response body: %v", err)
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return nil, fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return nil, ErrTooManyRequests
		}

		return nil, fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return nil, fmt.Errorf("failed to read response body: %v", err)
		}

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return nil, fmt.Errorf("failed to read 200 response body: %v", err)
	}

	var respBody TrackCreditsResponse
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		return nil, fmt.Errorf("failed to decode track credits 200 response body: %v", err)
	}

	return ptr.Of(respBody.toTrackCredits()), nil
}

func (d *Downloader) downloadTrackLyrics(ctx context.Context, accessToken string, id string) (l string, err error) {
	trackLyricsURL := fmt.Sprintf(trackLyricsAPIFormat, id)
	reqURL, err := url.Parse(trackLyricsURL)
	if nil != err {
		return "", fmt.Errorf("failed to parse track lyrics URL %s: %v", trackLyricsURL, err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("countryCode", "US")
	reqParams.Add("includeContributors", "true")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		return "", fmt.Errorf("failed to create get track lyrics request %s: %v", reqURL.String(), err)
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetTrackLyrics) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", context.DeadlineExceeded
		}

		if errors.Is(err, context.Canceled) {
			return "", context.Canceled
		}

		return "", fmt.Errorf("failed to send get track lyrics request: %v", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			err = errors.Join(err, fmt.Errorf("failed to close get track lyrics response body: %v", closeErr))
		}
	}()

	switch code := resp.StatusCode; code {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", nil
	case http.StatusUnauthorized:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return "", fmt.Errorf("failed to read 401 response body: %v", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			return "", fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return "", auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			return "", fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return "", auth.ErrUnauthorized
		}

		return "", fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return "", ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return "", fmt.Errorf("failed to read 403 response body: %v", err)
		}
		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			return "", fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return "", ErrTooManyRequests
		}

		return "", fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			return "", fmt.Errorf("failed to read response body: %v", err)
		}

		return "", fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		return "", fmt.Errorf("failed to read 200 response body: %v", err)
	}

	if !gjson.ValidBytes(respBytes) {
		return "", fmt.Errorf("invalid track lyrics 200 response json: %v", err)
	}

	var lyrics string
	if lyricsKey := gjson.GetBytes(respBytes, "subtitles"); lyricsKey.Type == gjson.String {
		lyrics = lyricsKey.Str
	} else if lyricsKey := gjson.GetBytes(respBytes, "lyrics"); lyricsKey.Type == gjson.String {
		lyrics = lyricsKey.Str
	} else {
		return "", fmt.Errorf("unexpected track lyrics 200 response: %v", err)
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

func embedTrackAttributes(ctx context.Context, trackFilePath string, attrs TrackEmbeddedAttrs) (err error) {
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
