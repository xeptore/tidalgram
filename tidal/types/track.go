package types

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	"github.com/samber/lo"
)

const (
	ReleaseDateLayout = "2006/01/02"
)

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
	return zerolog.
		Dict().
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

type ContainerInfo struct {
	Muxer     string // ffmpeg -f value (e.g. "mp4", "flac", "matroska")
	Extension string // file extension without dot (e.g. "m4a", "flac", "mka")
}

func ResolveContainer(mimeType, codec string) (ContainerInfo, error) {
	switch mimeType {
	case "audio/mp4":
		switch codec {
		case "eac3", "aac", "alac", "mp4a.40.2", "mp4a.40.5":
			return ContainerInfo{Muxer: "mp4", Extension: "m4a"}, nil
		case "flac":
			// Important: must force mp4 muxer (not ipod)
			return ContainerInfo{Muxer: "mp4", Extension: "m4a"}, nil
		}
	case "audio/flac":
		if codec == "flac" {
			return ContainerInfo{Muxer: "flac", Extension: "flac"}, nil
		}
	}

	return ContainerInfo{}, fmt.Errorf("unsupported mimeType=%s codec=%s", mimeType, codec)
}
