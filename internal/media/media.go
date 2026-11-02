// Package media describes playable items and the variants prepared for them.
package media

import "encoding/json"

// Item is what the room is currently showing.
type Item struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Duration float64    `json:"duration"` // seconds
	Variants []Variant  `json:"variants"`
	Subs     []Subtitle `json:"subs"`
	// ImageSubs counts embedded picture-based subtitle tracks that were
	// skipped (browsers can't render them as text).
	ImageSubs int `json:"imageSubs"`
}

// Variant is one prepared, directly playable MP4.
type Variant struct {
	ID       string `json:"id"`
	Label    string `json:"label"`  // e.g. "4K · HDR · original"
	Codecs   string `json:"codecs"` // RFC 6381, e.g. `hvc1.2.4.L153.B0, mp4a.40.2`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	HDR      bool   `json:"hdr"`
	Bitrate  int64  `json:"bitrate"`  // bits/s, average
	Lossless bool   `json:"lossless"` // video stream is a bit-exact copy of the source
	Path     string `json:"-"`
}

// Subtitle is a WebVTT track held in memory (subtitles are small).
type Subtitle struct {
	ID    string `json:"id"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
	VTT   []byte `json:"-"`
}

// Variant returns the variant with the given ID.
func (it *Item) Variant(id string) (Variant, bool) {
	for _, v := range it.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// Subtitle returns the subtitle with the given ID.
func (it *Item) Subtitle(id string) (Subtitle, bool) {
	for _, s := range it.Subs {
		if s.ID == id {
			return s, true
		}
	}
	return Subtitle{}, false
}

// MarshalJSON guarantees arrays, never null: clients index into them.
func (it Item) MarshalJSON() ([]byte, error) {
	type plain Item
	if it.Variants == nil {
		it.Variants = []Variant{}
	}
	if it.Subs == nil {
		it.Subs = []Subtitle{}
	}
	return json.Marshal(plain(it))
}
