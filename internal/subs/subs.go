// Package subs converts SubRip (.srt) subtitles to WebVTT.
package subs

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"
)

// timing matches "00:01:02,345 --> 00:01:04,567" with optional cue settings,
// tolerating '.' or ',' separators and 1–3 digit hours.
var timing = regexp.MustCompile(`^\s*(\d{1,3}):(\d{2}):(\d{2})[,.](\d{1,3})\s*-->\s*(\d{1,3}):(\d{2}):(\d{2})[,.](\d{1,3})`)

// SRTToVTT converts SRT bytes into a WebVTT document. Input that is not valid
// UTF-8 is decoded as Windows-1252 (the usual legacy encoding for Western
// European subtitles).
func SRTToVTT(in []byte) []byte {
	text := decode(in)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var out bytes.Buffer
	out.WriteString("WEBVTT\n\n")
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		// Find the timing line (index line is optional and dropped).
		ti := -1
		for i, l := range lines {
			if timing.MatchString(l) {
				ti = i
				break
			}
		}
		if ti < 0 {
			continue
		}
		m := timing.FindStringSubmatch(lines[ti])
		out.WriteString(ts(m[1], m[2], m[3], m[4]))
		out.WriteString(" --> ")
		out.WriteString(ts(m[5], m[6], m[7], m[8]))
		out.WriteByte('\n')
		for _, l := range lines[ti+1:] {
			if strings.TrimSpace(l) == "" {
				continue
			}
			// "-->" inside cue text would terminate parsing in WebVTT.
			out.WriteString(strings.ReplaceAll(l, "-->", "->"))
			out.WriteByte('\n')
		}
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func ts(h, m, s, ms string) string {
	for len(h) < 2 {
		h = "0" + h
	}
	for len(ms) < 3 {
		ms += "0"
	}
	return h + ":" + m + ":" + s + "." + ms
}

func decode(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c >= 0x80 && c <= 0x9F {
			sb.WriteRune(cp1252[c-0x80])
		} else {
			sb.WriteRune(rune(c)) // Latin-1 range matches Unicode
		}
	}
	return sb.String()
}

// cp1252 maps 0x80–0x9F; undefined bytes map to U+FFFD.
var cp1252 = [32]rune{
	'€', '�', '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', '�', 'Ž', '�',
	'�', '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', '�', 'ž', 'Ÿ',
}

// DecodeText returns b as UTF-8 text, stripping a BOM and decoding legacy
// Windows-1252 bytes if b is not valid UTF-8.
func DecodeText(b []byte) string { return decode(b) }
