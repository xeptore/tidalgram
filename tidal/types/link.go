package types

type LinkKind int

func (k LinkKind) String() string {
	switch k {
	case LinkKindPlaylist:
		return "Playlist"
	case LinkKindMix:
		return "Mix"
	case LinkKindAlbum:
		return "Album"
	case LinkKindTrack:
		return "Track"
	case LinkKindArtist:
		return "Artist"
	case LinkKindVideo:
		return "Video"
	}

	return "unknown"
}

const (
	LinkKindPlaylist LinkKind = iota
	LinkKindMix
	LinkKindAlbum
	LinkKindTrack
	LinkKindArtist
	LinkKindVideo
)

type Link struct {
	Kind LinkKind
	ID   string
}
