package types

import (
	"fmt"
	"strconv"
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

func (t Track) UploadTitle() string {
	title := t.Title
	if nil != t.Version {
		title += " (" + *t.Version + ")"
	}

	return title
}

func (t Track) UploadFilename() string {
	artistName := JoinArtists(t.Artists)
	if nil != t.Version {
		return fmt.Sprintf("%s - %s (%s).%s", artistName, t.Title, *t.Version, t.Ext)
	}

	return fmt.Sprintf("%s - %s.%s", artistName, t.Title, t.Ext)
}

type StoredTrack struct {
	Track

	Caption string `json:"caption"`
}

type StoredAlbumTrack struct {
	Track

	Index int `json:"index"`
}

func (t StoredAlbumTrack) UploadTitle() string {
	title := strconv.Itoa(t.Index+1) + ". " + t.Title
	if nil != t.Version {
		title += " (" + *t.Version + ")"
	}

	return title
}

func (t StoredAlbumTrack) UploadFilename() string {
	artistName := JoinArtists(t.Artists)
	if nil != t.Version {
		return fmt.Sprintf("%d. %s - %s (%s).%s", t.Index+1, artistName, t.Title, *t.Version, t.Ext)
	}

	return fmt.Sprintf("%d. %s - %s.%s", t.Index+1, artistName, t.Title, t.Ext)
}

type StoredPlaylist struct {
	Caption  string   `json:"caption"`
	TrackIDs []string `json:"track_ids"`
}

type StoredAlbum struct {
	Caption        string     `json:"caption"`
	VolumeTrackIDs [][]string `json:"volume_track_ids"`
}
