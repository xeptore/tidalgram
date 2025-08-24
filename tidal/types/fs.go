package types

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
