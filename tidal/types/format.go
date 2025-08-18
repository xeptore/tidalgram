package types

import (
	"fmt"
	"strings"
)

type TrackFormat struct {
	MimeType string `json:"mime_type"`
	Codec    string `json:"codec"`
}

func (f TrackFormat) InferTrackExt() string {
	ext, err := InferTrackExt(f.MimeType, f.Codec)
	if nil != err {
		panic(fmt.Sprintf("unsupported mime type %q", f.MimeType))
	}

	return ext
}

const (
	codecFLAC = "flac"
	extFLAC   = "flac"
)

func InferTrackExt(mimeType, codec string) (string, error) {
	switch mimeType {
	case "audio/mp4":
		switch strings.ToLower(codec) {
		case "eac3", "aac", "alac", "mp4a.40.2":
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
			return "", fmt.Errorf("unsupported codec %q for audio/mp4 mime type", codec)
		}
	default:
		return "", fmt.Errorf("unsupported mime type %q", mimeType)
	}
}
