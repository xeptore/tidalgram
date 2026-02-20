package types

type LinkKind int

func (k LinkKind) String() string {
	switch k {
	case LinkKindPlaylist:
		return "playlist"
	case LinkKindMix:
		return "mix"
	case LinkKindAlbum:
		return "album"
	case LinkKindTrack:
		return "track"
	case LinkKindArtist:
		return "artist"
	case LinkKindArtistCredits:
		return "credits"
	case LinkKindVideo:
		return "video"
	}

	return "unknown"
}

const (
	LinkKindPlaylist LinkKind = iota
	LinkKindMix
	LinkKindAlbum
	LinkKindTrack
	LinkKindArtist
	LinkKindArtistCredits
	LinkKindVideo
)

type Link struct {
	Kind LinkKind
	ID   string
}
