package types

import (
	"strings"

	"github.com/samber/lo"
)

const ReleaseDateLayout = "2006/01/02"

func JoinNames(names []string) string {
	return strings.Join(names, ", ")
}

type TrackCredits struct {
	Producers           []string
	Composers           []string
	Lyricists           []string
	AdditionalProducers []string
}

type TrackArtist struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

const (
	ArtistTypeMain     = "MAIN"
	ArtistTypeFeatured = "FEATURED"
)

func JoinArtists(artists []TrackArtist) string {
	mainArtists := lo.FilterMap(
		artists,
		func(a TrackArtist, _ int) (string, bool) { return a.Name, a.Type == ArtistTypeMain },
	)
	featArtists := lo.FilterMap(
		artists,
		func(a TrackArtist, _ int) (string, bool) { return a.Name, a.Type == ArtistTypeFeatured },
	)
	out := strings.Join(mainArtists, ", ")
	if len(featArtists) > 0 {
		out += " (feat. " + strings.Join(featArtists, ", ") + ")"
	}

	return out
}
