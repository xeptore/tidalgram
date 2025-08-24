package downloader

import (
	"bytes"
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
	"github.com/rs/zerolog"
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

func (d *Downloader) track(ctx context.Context, logger zerolog.Logger, id string) (err error) {
	accessToken := d.auth.Credentials().Token
	track, err := getTrackMeta(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to get track meta: %w", err)
	}

	trackFs := d.dir.Track(id)
	if exists, err := trackFs.Cover.Exists(); nil != err {
		logger.Error().Err(err).Msg("Failed to check if track cover exists")
		return fmt.Errorf("failed to check if track cover exists: %v", err)
	} else if !exists {
		coverBytes, err := d.getCover(ctx, logger, accessToken, track.CoverID)
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
				if !errors.Is(removeErr, os.ErrNotExist) {
					logger.Error().Err(removeErr).Msg("Failed to remove track file")
					err = errors.Join(err, fmt.Errorf("failed to remove track file: %v", removeErr))
				}
			}
		}
	}()

	ext, err := d.downloadTrack(ctx, logger, accessToken, id, trackFs.Path)
	if nil != err {
		return fmt.Errorf("failed to download track: %w", err)
	}

	trackCredits, err := d.getTrackCredits(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to get track credits: %w", err)
	}

	trackLyrics, err := d.downloadTrackLyrics(ctx, logger, accessToken, id)
	if nil != err {
		return fmt.Errorf("failed to download track lyrics: %w", err)
	}

	album, err := d.getAlbumMeta(ctx, logger, accessToken, track.AlbumID)
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
	if err := embedTrackAttributes(ctx, logger, trackFs.Path, attrs); nil != err {
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
		Caption: trackCaption(album.Title, album.ReleaseDate),
	}
	if err := trackFs.InfoFile.Write(info); nil != err {
		logger.Error().Err(err).Msg("Failed to write track info file")
		return fmt.Errorf("failed to write track info: %v", err)
	}

	return nil
}

func getTrackMeta(ctx context.Context, logger zerolog.Logger, accessToken, id string) (m *TrackMeta, err error) {
	trackURL := fmt.Sprintf(trackAPIFormat, id)
	reqURL, err := url.Parse(trackURL)
	must.NilErr(err)

	reqParams := make(url.Values, 1)
	reqParams.Add("countryCode", "US")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track info request")
		return nil, fmt.Errorf("failed to create get track info request: %w", err)
	}

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{Timeout: 5 * time.Second} //nolint:exhaustruct
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track info request")
		return nil, fmt.Errorf("failed to send get track info request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track info response body")
			err = fmt.Errorf("failed to close get track info response body: %v", closeErr)
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

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, fmt.Errorf("failed to read 200 response body: %w", err)
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
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode track info 200 response body")
		return nil, fmt.Errorf("failed to decode track info 200 response body: %w", err)
	}

	artists := make([]types.TrackArtist, len(respBody.Artists))
	for i, artist := range respBody.Artists {
		switch artist.Type {
		case types.ArtistTypeMain, types.ArtistTypeFeatured:
		default:
			logger.Error().Str("artist_type", artist.Type).Msg("Unexpected artist type")
			return nil, fmt.Errorf(
				"unexpected artist type: %s, expected: %s or %s",
				artist.Type,
				types.ArtistTypeMain,
				types.ArtistTypeFeatured,
			)
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

func (d *Downloader) downloadTrack(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
	fileName string,
) (ext string, err error) {
	logger = logger.With().Str("file_name", fileName).Logger()

	stream, ext, err := d.getStream(ctx, logger, accessToken, id)
	if nil != err {
		return "", fmt.Errorf("failed to get track stream: %w", err)
	}

	time.Sleep(ratelimit.TrackDownloadSleepMS())

	if err := stream.saveTo(ctx, logger, accessToken, fileName); nil != err {
		return "", fmt.Errorf("failed to download track: %w", err)
	}

	return ext, nil
}

func trackCaption(albumTitle string, releaseDate time.Time) string {
	return fmt.Sprintf("%s (%s)", albumTitle, releaseDate.Format(types.ReleaseDateLayout))
}

func (d *Downloader) getTrackCredits(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) (*types.TrackCredits, error) {
	cachedTrackCredits, err := d.cache.TrackCredits.Fetch(
		id,
		cache.DefaultTrackCreditsTTL,
		func() (*types.TrackCredits, error) { return d.downloadTrackCredits(ctx, logger, accessToken, id) },
	)
	if nil != err {
		return nil, fmt.Errorf("failed to get track credits: %w", err)
	}

	return cachedTrackCredits.Value(), nil
}

func (d *Downloader) downloadTrackCredits(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) (c *types.TrackCredits, err error) {
	trackCreditsURL := fmt.Sprintf(trackCreditsAPIFormat, id)
	reqURL, err := url.Parse(trackCreditsURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse track credits URL")
		return nil, fmt.Errorf("failed to parse track credits URL %s: %v", trackCreditsURL, err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("countryCode", "US")
	reqParams.Add("includeContributors", "true")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track credits request")
		return nil, fmt.Errorf("failed to create get track credits request %s: %w", reqURL.String(), err)
	}

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetTrackCredits) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track credits request")
		return nil, fmt.Errorf("failed to send get track credits request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track credits response body")
			err = errors.Join(err, fmt.Errorf("failed to close get track credits response body: %v", closeErr))
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

		return nil, fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return nil, fmt.Errorf("failed to read 200 response body: %w", err)
	}

	var respBody TrackCreditsResponse
	if err := json.Unmarshal(respBytes, &respBody); nil != err {
		logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to decode track credits 200 response body")
		return nil, fmt.Errorf("failed to decode track credits 200 response body: %w", err)
	}

	return ptr.Of(respBody.toTrackCredits()), nil
}

func (d *Downloader) downloadTrackLyrics(
	ctx context.Context,
	logger zerolog.Logger,
	accessToken string,
	id string,
) (l string, err error) {
	trackLyricsURL := fmt.Sprintf(trackLyricsAPIFormat, id)
	reqURL, err := url.Parse(trackLyricsURL)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to parse track lyrics URL")
		return "", fmt.Errorf("failed to parse track lyrics URL %s: %v", trackLyricsURL, err)
	}

	reqParams := make(url.Values, 2)
	reqParams.Add("countryCode", "US")
	reqParams.Add("includeContributors", "true")
	reqURL.RawQuery = reqParams.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to create get track lyrics request")
		return "", fmt.Errorf("failed to create get track lyrics request %s: %w", reqURL.String(), err)
	}

	req.Header.Add("Accept", "application/json")
	req.Header.Add("Authorization", "Bearer "+accessToken)

	client := http.Client{ //nolint:exhaustruct
		Timeout: time.Duration(d.conf.Timeouts.GetTrackLyrics) * time.Second,
	}
	resp, err := client.Do(req)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to send get track lyrics request")
		return "", fmt.Errorf("failed to send get track lyrics request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); nil != closeErr {
			logger.Error().Err(closeErr).Msg("Failed to close get track lyrics response body")
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
			logger.Error().Err(err).Msg("Failed to read 401 response body")
			return "", fmt.Errorf("failed to read 401 response body: %w", err)
		}

		if ok, err := httputil.IsTokenExpiredResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token expired")
			return "", fmt.Errorf("failed to check if 401 response is token expired: %v", err)
		} else if ok {
			return "", auth.ErrUnauthorized
		}

		if ok, err := httputil.IsTokenInvalidResponse(respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 401 response is token invalid")
			return "", fmt.Errorf("failed to check if 401 response is token invalid: %v", err)
		} else if ok {
			return "", auth.ErrUnauthorized
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 401 response")

		return "", fmt.Errorf("unexpected 401 response with body: %s", string(respBytes))
	case http.StatusTooManyRequests:
		return "", ErrTooManyRequests
	case http.StatusForbidden:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Msg("Failed to read 403 response body")
			return "", fmt.Errorf("failed to read 403 response body: %w", err)
		}

		if ok, err := httputil.IsTooManyErrorResponse(resp, respBytes); nil != err {
			logger.Error().Err(err).Bytes("response_body", respBytes).Msg("Failed to check if 403 response is too many requests")
			return "", fmt.Errorf("failed to check if 403 response is too many requests: %v", err)
		} else if ok {
			return "", ErrTooManyRequests
		}

		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected 403 response")

		return "", fmt.Errorf("unexpected 403 response with body: %s", string(respBytes))
	default:
		respBytes, err := io.ReadAll(resp.Body)
		if nil != err {
			logger.Error().Err(err).Int("status_code", code).Msg("Failed to read response body")
			return "", fmt.Errorf("failed to read response body: %w", err)
		}

		logger.Error().Int("status_code", code).Bytes("response_body", respBytes).Msg("Unexpected response status code")

		return "", fmt.Errorf("unexpected status code %d with body: %s", code, string(respBytes))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if nil != err {
		logger.Error().Err(err).Msg("Failed to read 200 response body")
		return "", fmt.Errorf("failed to read 200 response body: %w", err)
	}

	if !gjson.ValidBytes(respBytes) {
		logger.Error().Bytes("response_body", respBytes).Msg("Invalid track lyrics 200 response json")
		return "", fmt.Errorf("invalid track lyrics 200 response json: %v", err)
	}

	var lyrics string
	if lyricsKey := gjson.GetBytes(respBytes, "subtitles"); lyricsKey.Type == gjson.String {
		lyrics = lyricsKey.Str
	} else if lyricsKey := gjson.GetBytes(respBytes, "lyrics"); lyricsKey.Type == gjson.String {
		lyrics = lyricsKey.Str
	} else {
		logger.Error().Bytes("response_body", respBytes).Msg("Unexpected track lyrics 200 response")
		return "", fmt.Errorf("unexpected track lyrics 200 response: %s", string(respBytes))
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
	Ext          string
}

func (t TrackEmbeddedAttrs) toDict() *zerolog.Event {
	return zerolog.Dict().
		Str("lead_artist", t.LeadArtist).
		Str("album", t.Album).
		Str("album_artist", t.AlbumArtist).
		Strs("artists", lo.Map(t.Artists, func(a types.TrackArtist, _ int) string { return a.Name })).
		Str("copyright", t.Copyright).
		Str("cover_path", t.CoverPath).
		Str("isrc", t.ISRC).
		Time("release_date", t.ReleaseDate).
		Str("title", t.Title).
		Int("track_number", t.TrackNumber).
		Int("total_tracks", t.TotalTracks).
		Int("volume_number", t.VolumeNumber).
		Int("total_volumes", t.TotalVolumes).
		Dict("credits", t.Credits.ToDict()).
		Str("lyrics", t.Lyrics).
		Str("version", ptr.ValueOr(t.Version, "<nil>")).
		Str("ext", t.Ext)
}

func embedTrackAttributes(
	ctx context.Context,
	logger zerolog.Logger,
	trackFilePath string,
	attrs TrackEmbeddedAttrs,
) (err error) {
	logger = logger.With().Str("track_file_path", trackFilePath).Dict("attrs", attrs.toDict()).Logger()

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

	trackFilenameExt := trackFilePath + "." + attrs.Ext

	args := []string{
		"-i",
		trackFilePath,
		"-i",
		attrs.CoverPath,
		"-map",
		"0:a",
		"-map",
		"1:v",
		"-c",
		"copy",
		"-disposition:v:0",
		"attached_pic",
	}
	args = append(args, metaArgs...)
	args = append(args, trackFilenameExt)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	logger.Debug().Strs("args", args).Msg("Running ffmpeg")

	var (
		stdOut bytes.Buffer
		stdErr bytes.Buffer
	)

	cmd.Stdout = &stdOut
	cmd.Stderr = &stdErr

	if err := cmd.Run(); nil != err {
		if errors.Is(err, exec.ErrNotFound) {
			logger.Error().Err(err).Msg("ffmpeg not found")
			return fmt.Errorf("ffmpeg not found: %v", err)
		}

		logger.Error().Err(err).Str("stderr", stdErr.String()).Msg("ffmpeg failed")

		return fmt.Errorf("failed to write track attributes using ffmpeg (%w): %s", err, stdErr.String())
	}

	if err := os.Rename(trackFilenameExt, trackFilePath); nil != err {
		logger.Error().Err(err).Msg("Failed to rename track file")
		return fmt.Errorf("failed to rename track file: %v", err)
	}

	return nil
}
