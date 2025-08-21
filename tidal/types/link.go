package types

type LinkKind int

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
