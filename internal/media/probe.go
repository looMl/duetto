package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"duetto/internal/procgroup"
)

// Probe is the subset of `ffprobe -show_format -show_streams` that we use.
type Probe struct {
	Format  ProbeFormat   `json:"format"`
	Streams []ProbeStream `json:"streams"`
}

type ProbeFormat struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
}

type ProbeStream struct {
	Index          int               `json:"index"`
	CodecName      string            `json:"codec_name"`
	CodecType      string            `json:"codec_type"`
	Profile        string            `json:"profile"`
	Level          int               `json:"level"`
	Width          int               `json:"width"`
	Height         int               `json:"height"`
	PixFmt         string            `json:"pix_fmt"`
	ColorTransfer  string            `json:"color_transfer"`
	ColorPrimaries string            `json:"color_primaries"`
	Channels       int               `json:"channels"`
	BitRate        string            `json:"bit_rate"`
	Disposition    map[string]int    `json:"disposition"`
	Tags           map[string]string `json:"tags"`
}

// RunProbe runs ffprobe on path.
func RunProbe(ctx context.Context, ffprobe, path string) (*Probe, error) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path)
	procgroup.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("ffprobe: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	var p Probe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	return &p, nil
}

// Duration in seconds (0 if unknown).
func (p *Probe) Duration() float64 {
	d, _ := strconv.ParseFloat(p.Format.Duration, 64)
	return d
}

// BitRate in bits/s (0 if unknown).
func (p *Probe) BitRate() int64 {
	b, _ := strconv.ParseInt(p.Format.BitRate, 10, 64)
	return b
}

// Video returns the main video stream (skipping cover art).
func (p *Probe) Video() (ProbeStream, bool) {
	for _, s := range p.Streams {
		if s.CodecType == "video" && s.Disposition["attached_pic"] == 0 {
			return s, true
		}
	}
	return ProbeStream{}, false
}

// Audio returns the audio streams in file order.
func (p *Probe) Audio() []ProbeStream { return p.ofType("audio") }

// Subtitles returns the subtitle streams in file order.
func (p *Probe) Subtitles() []ProbeStream { return p.ofType("subtitle") }

func (p *Probe) ofType(t string) []ProbeStream {
	var out []ProbeStream
	for _, s := range p.Streams {
		if s.CodecType == t {
			out = append(out, s)
		}
	}
	return out
}

// HDR reports whether the stream uses a high-dynamic-range transfer.
func (s ProbeStream) HDR() bool {
	return s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67"
}

// TenBit reports a >8-bit pixel format.
func (s ProbeStream) TenBit() bool {
	return strings.Contains(s.PixFmt, "10") || strings.Contains(s.PixFmt, "12")
}

// Lang returns the ISO 639-2 language tag, or "".
func (s ProbeStream) Lang() string {
	l := strings.ToLower(s.Tags["language"])
	if l == "und" {
		return ""
	}
	return l
}
