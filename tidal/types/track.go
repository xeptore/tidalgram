package types

import (
	"strings"

	"github.com/rs/zerolog"
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

func (t TrackCredits) ToDict() *zerolog.Event {
	return zerolog.Dict().
		Strs("producers", t.Producers).
		Strs("composers", t.Composers).
		Strs("lyricists", t.Lyricists).
		Strs("additional_producers", t.AdditionalProducers)
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
