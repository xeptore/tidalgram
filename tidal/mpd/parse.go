package mpd

import (
	"encoding/xml"
	"fmt"
	"io"
)

type MPD struct {
	XMLName                   xml.Name `xml:"MPD"`
	Profiles                  string   `xml:"profiles,attr"`
	Type                      string   `xml:"type,attr"`
	MinBufferTime             string   `xml:"minBufferTime,attr"`
	MediaPresentationDuration string   `xml:"mediaPresentationDuration,attr"`
	Period                    Period   `xml:"Period"`
}

type Period struct {
	ID            string        `xml:"id,attr"`
	AdaptationSet AdaptationSet `xml:"AdaptationSet"`
}

type AdaptationSet struct {
	ID               string         `xml:"id,attr"`
	ContentType      string         `xml:"contentType,attr"`
	MimeType         string         `xml:"mimeType,attr"`
	SegmentAlignment bool           `xml:"segmentAlignment,attr"`
	Representation   Representation `xml:"Representation"`
}

type Representation struct {
	ID                string          `xml:"id,attr"`
	Codecs            string          `xml:"codecs,attr"`
	Bandwidth         int             `xml:"bandwidth,attr"`
	AudioSamplingRate int             `xml:"audioSamplingRate,attr"`
	SegmentTemplate   SegmentTemplate `xml:"SegmentTemplate"`
}

type SegmentTemplate struct {
	Timescale       int             `xml:"timescale,attr"`
	Initialization  string          `xml:"initialization,attr"`
	Media           string          `xml:"media,attr"`
	StartNumber     int             `xml:"startNumber,attr"`
	SegmentTimeline SegmentTimeline `xml:"SegmentTimeline"`
}

type SegmentTimeline struct {
	S []S `xml:"S"`
}

type S struct {
	D int `xml:"d,attr"`
	R int `xml:"r,attr,omitempty"`
}

type StreamInfo struct {
	Codec    string
	MimeType string
	Parts    Parts
}

type Parts struct {
	InitializationURLTemplate string
	Count                     int
}

func (m *MPD) parts() (*Parts, error) {
	contentType := m.Period.AdaptationSet.ContentType
	if contentType != "audio" {
		return nil, fmt.Errorf("unexpected content type: %s", contentType)
	}

	partsCount := 2
	for _, s := range m.Period.AdaptationSet.Representation.SegmentTemplate.SegmentTimeline.S {
		if s.R != 0 {
			partsCount += s.R
		} else {
			partsCount++
		}
	}

	return &Parts{
		InitializationURLTemplate: m.Period.AdaptationSet.Representation.SegmentTemplate.Media,
		Count:                     partsCount,
	}, nil
}

func ParseStreamInfo(r io.Reader) (*StreamInfo, error) {
	var mpd MPD
	dec := xml.NewDecoder(r)
	dec.Strict = true
	if err := dec.Decode(&mpd); nil != err {
		return nil, fmt.Errorf("failed to parse MPD: %v", err)
	}

	parts, err := mpd.parts()
	if nil != err {
		return nil, fmt.Errorf("failed to get parts: %v", err)
	}

	return &StreamInfo{
		Codec:    mpd.Period.AdaptationSet.Representation.Codecs,
		MimeType: mpd.Period.AdaptationSet.MimeType,
		Parts:    *parts,
	}, nil
}
