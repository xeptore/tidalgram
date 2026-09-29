package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/rs/zerolog"

	"github.com/xeptore/tidalgram/tidal/fs"
	"github.com/xeptore/tidalgram/tidal/types"
)

// downloadsTrackProvider reads the track info file and audio produced by the downloader.
// Download stays a separate earlier step; this is the metadata and audio source at send time.
type downloadsTrackProvider struct {
	dir    fs.DownloadsDir
	logger zerolog.Logger
}

func newDownloadsTrackProvider(logger zerolog.Logger, dir fs.DownloadsDir) downloadsTrackProvider {
	return downloadsTrackProvider{
		dir:    dir,
		logger: logger,
	}
}

func (p downloadsTrackProvider) TrackMeta(ctx context.Context, trackID string) (types.StoredTrack, error) {
	if err := ctx.Err(); nil != err {
		return types.StoredTrack{}, fmt.Errorf("track metadata: %w", err)
	}

	info, err := p.dir.Track(trackID).InfoFile.Read()
	if nil != err {
		p.logger.Error().Err(err).Str("track_id", trackID).Msg("Failed to read track info file")
		return types.StoredTrack{}, fmt.Errorf("read track info file: %w", err)
	}

	return *info, nil
}

func (p downloadsTrackProvider) TrackAudio(ctx context.Context, trackID string) (TrackAudio, error) {
	if err := ctx.Err(); nil != err {
		return TrackAudio{}, fmt.Errorf("track audio: %w", err)
	}

	track := p.dir.Track(trackID)

	trackStat, err := os.Lstat(track.Path)
	if nil != err {
		p.logger.Error().Err(err).Str("track_id", trackID).Msg("Failed to stat track file")
		return TrackAudio{}, fmt.Errorf("stat track file: %w", err)
	}
	if !trackStat.Mode().IsRegular() {
		return TrackAudio{}, fmt.Errorf("track file %q is not a regular file", track.Path)
	}
	if trackStat.Size() == 0 {
		return TrackAudio{}, errors.New("track file is empty")
	}

	coverStat, err := os.Lstat(track.Cover.Path)
	if nil != err {
		p.logger.Error().Err(err).Str("track_id", trackID).Msg("Failed to stat track cover file")
		return TrackAudio{}, fmt.Errorf("stat track cover file: %w", err)
	}
	if !coverStat.Mode().IsRegular() {
		return TrackAudio{}, fmt.Errorf("track cover file %q is not a regular file", track.Cover.Path)
	}
	if coverStat.Size() == 0 {
		return TrackAudio{}, errors.New("track cover file is empty")
	}

	return TrackAudio{
		Path:      track.Path,
		CoverPath: track.Cover.Path,
	}, nil
}
