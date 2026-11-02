package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"duetto/internal/procgroup"
	"duetto/internal/subs"
)

// MaxSubtitleSize bounds uploaded subtitle files.
const MaxSubtitleSize = 10 << 20

// ToVTT converts an uploaded subtitle file (by extension) to WebVTT.
// SRT and VTT are converted in-process; ASS/SSA go through ffmpeg.
func ToVTT(ctx context.Context, ffmpeg, name string, data []byte) ([]byte, error) {
	var vtt []byte
	switch strings.ToLower(filepath.Ext(name)) {
	case ".srt":
		vtt = subs.SRTToVTT(data)
	case ".vtt":
		vtt = []byte(subs.DecodeText(data))
		if !strings.HasPrefix(string(vtt), "WEBVTT") {
			return nil, errors.New("this .vtt file has no WEBVTT header")
		}
	case ".ass", ".ssa":
		tmp, err := os.CreateTemp("", "duetto-sub-*"+filepath.Ext(name))
		if err != nil {
			return nil, err
		}
		defer os.Remove(tmp.Name())
		_, err = tmp.Write(data)
		tmp.Close()
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-v", "error", "-i", tmp.Name(), "-f", "webvtt", "pipe:1")
		procgroup.Hide(cmd)
		if vtt, err = cmd.Output(); err != nil {
			return nil, errors.New("could not read this subtitle file")
		}
	default:
		return nil, errors.New("use an .srt, .vtt, .ass or .ssa file")
	}
	if !bytes.Contains(vtt, []byte("-->")) {
		return nil, errors.New("no subtitles found in this file")
	}
	return vtt, nil
}

var langTokens = map[string]string{
	"ita": "ita", "it": "ita", "italian": "ita", "italiano": "ita",
	"eng": "eng", "en": "eng", "english": "eng", "inglese": "eng",
	"por": "por", "pt": "por", "pob": "por", "ptbr": "por", "portuguese": "por", "portugues": "por", "português": "por",
	"spa": "spa", "es": "spa", "spanish": "spa", "español": "spa",
	"fre": "fre", "fra": "fre", "fr": "fre", "french": "fre",
	"ger": "ger", "deu": "ger", "de": "ger", "german": "ger",
}

var tokenSplit = regexp.MustCompile(`[^\p{L}]+`)

// GuessLang infers an ISO 639-2 code from a file name like
// "Movie.1980.ita.srt" or "Movie [English].srt"; "" if unknown. The last
// matching token wins (language tags usually come last). Two-letter codes
// count only as the final token, so "It.2017.srt" is not Italian.
func GuessLang(name string) string {
	stem := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	tokens := tokenSplit.Split(strings.ToLower(stem), -1)
	lang := ""
	for i, t := range tokens {
		l, ok := langTokens[t]
		if !ok || (len(t) == 2 && i != len(tokens)-1) {
			continue
		}
		lang = l
	}
	return lang
}

// UploadLabel picks a readable, unique label for an uploaded track.
func UploadLabel(lang string, existing []Subtitle) string {
	base := "Uploaded"
	if lang != "" {
		base = langName(lang)
	}
	taken := map[string]bool{}
	for _, s := range existing {
		taken[s.Label] = true
	}
	label := base
	for n := 2; taken[label]; n++ {
		label = fmt.Sprintf("%s (%d)", base, n)
	}
	return label
}

type uploadMeta struct {
	ID    string `json:"id"`
	Lang  string `json:"lang"`
	Label string `json:"label"`
}

// uploadsDir holds tracks uploaded for one item, so they survive restarts.
func uploadsDir(itemDir string) string { return filepath.Join(itemDir, "uploads") }

// SaveUpload persists an uploaded track next to the item's prepared files.
func SaveUpload(itemDir string, s Subtitle) error {
	dir := uploadsDir(itemDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	meta, _ := json.Marshal(uploadMeta{ID: s.ID, Lang: s.Lang, Label: s.Label})
	if err := os.WriteFile(filepath.Join(dir, s.ID+".vtt"), s.VTT, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, s.ID+".json"), meta, 0o644)
}

// LoadUploads returns previously uploaded tracks in upload order.
func LoadUploads(itemDir string) []Subtitle {
	metas, _ := filepath.Glob(filepath.Join(uploadsDir(itemDir), "u*.json"))
	var out []Subtitle
	for _, mp := range metas {
		b, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var m uploadMeta
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		vtt, err := os.ReadFile(strings.TrimSuffix(mp, ".json") + ".vtt")
		if err != nil {
			continue
		}
		out = append(out, Subtitle{ID: m.ID, Lang: m.Lang, Label: m.Label, VTT: vtt})
	}
	sort.Slice(out, func(i, j int) bool { return uploadNum(out[i].ID) < uploadNum(out[j].ID) })
	return out
}

// NextUploadID returns "u1", "u2", … not used by existing tracks.
func NextUploadID(existing []Subtitle) string {
	n := 0
	for _, s := range existing {
		if k := uploadNum(s.ID); k > n {
			n = k
		}
	}
	return "u" + strconv.Itoa(n+1)
}

func uploadNum(id string) int {
	if !strings.HasPrefix(id, "u") {
		return 0
	}
	n, _ := strconv.Atoi(id[1:])
	return n
}

// ItemDir is where an item's prepared files and uploads live.
func (pr *Preparer) ItemDir(itemID string) string { return filepath.Join(pr.CacheDir, itemID) }
