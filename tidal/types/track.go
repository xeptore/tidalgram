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

const (
	codecFLAC = "flac"
	extFLAC   = "flac"
)

func InferTrackExt(mimeType, codec string) (string, error) {
	switch mimeType {
	case "audio/mp4":
		switch strings.ToLower(codec) {
		case "eac3", "aac", "alac", "mp4a.40.2", "mp4a.40.5":
			return "m4a", nil
		case codecFLAC:
			return extFLAC, nil
		default:
			return "", fmt.Errorf("unsupported codec %q for audio/mp4 mime type", codec)
		}
	case "audio/flac":
		switch strings.ToLower(codec) {
		case codec:
			return extFLAC, nil
		default:
			return "", fmt.Errorf("unsupported codec %q for audio/flac mime type", codec)
		}
	default:
		return "", fmt.Errorf("unsupported mime type %q", mimeType)
	}
}

func FormatTrackQuality(codec string, bitDepth, sampleRate *int) string {
	name := codecDisplayName(codec)
	if nil == bitDepth || nil == sampleRate || *bitDepth == 0 || *sampleRate == 0 {
		return name
	}

	return fmt.Sprintf("%s | %dBit - %s", name, *bitDepth, formatSampleRate(*sampleRate))
}

func codecDisplayName(codec string) string {
	switch strings.ToLower(codec) {
	case codecFLAC:
		return "FLAC"
	case "aac", "mp4a.40.2", "mp4a.40.5":
		return "AAC"
	case "alac":
		return "ALAC"
	case "eac3":
		return "EAC3"
	default:
		return strings.ToUpper(codec)
	}
}

func formatSampleRate(hz int) string {
	switch hz {
	case 44100:
		return "44.1kHz"
	case 48000:
		return "48kHz"
	case 88200:
		return "88.2kHz"
	case 96000:
		return "96kHz"
	case 176400:
		return "176.4kHz"
	case 192000:
		return "192kHz"
	default:
		if hz%1000 == 0 {
			return fmt.Sprintf("%dkHz", hz/1000)
		}

		return fmt.Sprintf("%.1fkHz", float64(hz)/1000)
	}
}
