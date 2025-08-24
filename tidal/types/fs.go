package types

import (
	"fmt"
)

type StoredMix struct {
	Caption  string   `json:"caption"`
	TrackIDs []string `json:"track_ids"`
}

type Track struct {
	Artists  []TrackArtist `json:"artists"`
	Title    string        `json:"title"`
	Duration int           `json:"duration"`
	Version  *string       `json:"version"`
	CoverID  string        `json:"cover_id"`
	Ext      string        `json:"ext"`
}

func (t Track) filenameBase() string {
	artistName := JoinArtists(t.Artists)
	if nil != t.Version {
		return fmt.Sprintf("%s - %s (%s)", artistName, t.Title, *t.Version)
	}

	return fmt.Sprintf("%s - %s", artistName, t.Title)
}

func (t Track) Filename() string {
	return t.filenameBase() + "." + t.Ext
}

func (t Track) CoverFilename() string {
	return t.filenameBase() + "." + CoverExt
}

type StoredTrack struct {
	Track

	Caption string `json:"caption"`
}

type StoredPlaylist struct {
	Caption  string   `json:"caption"`
	TrackIDs []string `json:"track_ids"`
}

type StoredAlbum struct {
	Caption        string     `json:"caption"`
	VolumeTrackIDs [][]string `json:"volume_track_ids"`
}
