package media

import (
	"fmt"
	"strings"
)

// Encoder selects the H.264 encoder used for the compatibility variant.
type Encoder string

const (
	EncoderNVENC Encoder = "h264_nvenc"
	EncoderX264  Encoder = "libx264"
)

// PlanOptions tunes planning.
type PlanOptions struct {
	AudioLang string  // preferred ISO 639-2 audio language, e.g. "eng"; "" = default track
	Encoder   Encoder // for the compatibility variant
}

// Job produces one variant. Args use the placeholders {in} and {out}.
// A Direct job needs no processing: the source file is served as-is.
type Job struct {
	Variant Variant
	Direct  bool
	Args    []string
}

// Plan is the full preparation recipe for one source file.
type Plan struct {
	Jobs  []Job         // best first
	Audio ProbeStream   // chosen audio track
	Subs  []ProbeStream // embedded text subtitle tracks to extract
	// ImageSubs counts embedded picture-based tracks (PGS, VobSub) that
	// browsers cannot show as text.
	ImageSubs int
}

// compatHeight caps the compatibility variant's height.
const compatHeight = 1080

// MakePlan decides, without running anything, how to make the source
// playable in browsers with the least quality loss:
//
//  1. A passthrough variant whenever the video codec is decodable by some
//     browser: the video bitstream is copied (lossless), only audio is
//     transcoded if browsers can't play it, and the container becomes MP4.
//  2. A compatibility variant (H.264 8-bit SDR ≤1080p) whenever the
//     passthrough isn't universally playable (HEVC, AV1, VP9, HDR, 10-bit).
func MakePlan(p *Probe, o PlanOptions) (*Plan, error) {
	v, ok := p.Video()
	if !ok {
		return nil, fmt.Errorf("no video stream found")
	}
	audios := p.Audio()
	if len(audios) == 0 {
		return nil, fmt.Errorf("no audio stream found")
	}
	a := chooseAudio(audios, o.AudioLang)
	plan := &Plan{Audio: a}
	for _, s := range p.Subtitles() {
		if textSub(s.CodecName) {
			plan.Subs = append(plan.Subs, s)
		} else {
			plan.ImageSubs++
		}
	}

	audioOK := browserAudio(a.CodecName)
	audioCodec, audioArgs := "mp4a.40.2", []string{"-c:a", "aac", "-b:a", "256k", "-ac", "2"}
	if audioOK {
		audioCodec, audioArgs = audioCodecString(a.CodecName), []string{"-c:a", "copy"}
	}

	videoCodec, passthrough := videoCodecString(v)
	universal := v.CodecName == "h264" && !v.TenBit() && !v.HDR()
	bitrate := p.BitRate()

	if passthrough {
		pv := Variant{
			ID: "original", Label: originalLabel(v), Codecs: videoCodec + ", " + audioCodec,
			Width: v.Width, Height: v.Height, HDR: v.HDR(), Bitrate: bitrate, Lossless: true,
		}
		firstAudio := audios[0].Index == a.Index
		if universal && audioOK && firstAudio && strings.Contains(p.Format.FormatName, "mp4") {
			plan.Jobs = append(plan.Jobs, Job{Variant: pv, Direct: true})
		} else {
			args := []string{"-i", "{in}", "-map", "0:v:0", "-map", fmt.Sprintf("0:%d", a.Index), "-c:v", "copy"}
			if v.CodecName == "hevc" {
				args = append(args, "-tag:v", "hvc1") // required by Safari/Chrome for HEVC in MP4
			}
			args = append(args, audioArgs...)
			plan.Jobs = append(plan.Jobs, Job{Variant: pv, Args: withOutput(args)})
		}
	}

	if !universal {
		h := v.Height
		w := v.Width
		if h > compatHeight {
			w = (w*compatHeight/h + 1) &^ 1
			h = compatHeight
		}
		cv := Variant{
			ID: "compat", Label: fmt.Sprintf("%s · compatible", resLabel(w, h)), Codecs: "avc1.640029, " + audioCodec,
			Width: w, Height: h, Bitrate: 8_000_000,
		}
		var args []string
		if o.Encoder == EncoderNVENC {
			args = append(args, "-hwaccel", "cuda") // GPU decode; ffmpeg falls back to CPU if unavailable
		}
		args = append(args, "-i", "{in}", "-map", "0:v:0", "-map", fmt.Sprintf("0:%d", a.Index),
			"-vf", compatFilter(v), "-g", "120")
		args = append(args, encoderArgs(o.Encoder)...)
		args = append(args, audioArgs...)
		plan.Jobs = append(plan.Jobs, Job{Variant: cv, Args: withOutput(args)})
	}
	return plan, nil
}

func withOutput(args []string) []string {
	return append(args, "-sn", "-dn", "-map_chapters", "-1", "-movflags", "+faststart", "-f", "mp4",
		"-progress", "pipe:1", "-nostats", "-y", "{out}")
}

func encoderArgs(e Encoder) []string {
	common := []string{"-pix_fmt", "yuv420p", "-profile:v", "high", "-level", "4.1",
		"-maxrate", "12M", "-bufsize", "24M"}
	if e == EncoderNVENC {
		return append([]string{"-c:v", "h264_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr",
			"-cq", "21", "-b:v", "0", "-spatial-aq", "1"}, common...)
	}
	return append([]string{"-c:v", "libx264", "-preset", "medium", "-crf", "20"}, common...)
}

// compatFilter scales to ≤1080p first (cheap), then tone-maps HDR→SDR.
func compatFilter(v ProbeStream) string {
	var f []string
	if v.Height > compatHeight {
		scale := "scale=-2:%d"
		if v.HDR() {
			scale = "zscale=w=-2:h=%d" // HDR stays in zimg end to end
		}
		f = append(f, fmt.Sprintf(scale, compatHeight))
	}
	if v.HDR() {
		f = append(f, "zscale=t=linear:npl=100", "format=gbrpf32le", "zscale=p=bt709",
			"tonemap=tonemap=hable:desat=0", "zscale=t=bt709:m=bt709:r=tv")
	}
	return strings.Join(append(f, "format=yuv420p"), ",")
}

func chooseAudio(as []ProbeStream, lang string) ProbeStream {
	if lang != "" {
		for _, a := range as {
			if a.Lang() == lang {
				return a
			}
		}
	}
	for _, a := range as {
		if a.Disposition["default"] == 1 {
			return a
		}
	}
	return as[0]
}

func browserAudio(codec string) bool {
	switch codec {
	case "aac", "mp3", "opus", "flac":
		return true
	}
	return false
}

func audioCodecString(codec string) string {
	switch codec {
	case "mp3":
		return "mp4a.6B"
	case "opus":
		return "opus"
	case "flac":
		return "fLaC"
	}
	return "mp4a.40.2"
}

func textSub(codec string) bool {
	switch codec {
	case "subrip", "ass", "ssa", "mov_text", "webvtt", "text":
		return true
	}
	return false
}

// videoCodecString returns the RFC 6381 codec string and whether any
// mainstream browser can decode the stream as-is.
func videoCodecString(v ProbeStream) (string, bool) {
	switch v.CodecName {
	case "h264":
		if v.TenBit() {
			return "", false // Hi10P: no browser decodes it
		}
		prof := map[string]string{
			"Baseline": "42E0", "Constrained Baseline": "42E0", "Main": "4D40", "High": "6400",
		}[v.Profile]
		if prof == "" {
			prof = "6400"
		}
		return fmt.Sprintf("avc1.%s%02X", prof, max(v.Level, 0)), true
	case "hevc":
		if v.TenBit() {
			return fmt.Sprintf("hvc1.2.4.L%d.B0", v.Level), true
		}
		return fmt.Sprintf("hvc1.1.6.L%d.B0", v.Level), true
	case "av1":
		depth := "08"
		if v.TenBit() {
			depth = "10"
		}
		return "av01.0.08M." + depth, true
	case "vp9":
		return "vp09.00.40.08", true
	}
	return "", false
}

func originalLabel(v ProbeStream) string {
	if v.HDR() {
		return resLabel(v.Width, v.Height) + " · HDR · original"
	}
	return resLabel(v.Width, v.Height) + " · original"
}

// resLabel names a resolution class. Width decides: scope films (2.39:1)
// are 1920×800 yet are "1080p".
func resLabel(w, h int) string {
	switch {
	case w >= 3200 || h >= 2000:
		return "4K"
	case w >= 1800 || h >= 1000:
		return "1080p"
	case w >= 1200 || h >= 700:
		return "720p"
	default:
		return fmt.Sprintf("%dp", h)
	}
}
