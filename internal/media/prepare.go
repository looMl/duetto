package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"duetto/internal/procgroup"
	"duetto/internal/subs"
)

// Tools locates the ffmpeg binaries.
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// Progress is reported while preparing.
type Progress struct {
	Step      string  `json:"step"`
	StepIndex int     `json:"stepIndex"` // 1-based; 0 while analysing
	StepCount int     `json:"stepCount"`
	Fraction  float64 `json:"progress"` // 0..1 of the current step
	ETA       float64 `json:"eta"`      // seconds left in the current step; < 0 if unknown
}

// Result of a preparation.
type Result struct {
	Item *Item
	// Pending: planned variants not built because withCompat was false.
	Pending []Variant
}

// Preparer turns a source file into a playable Item, caching outputs.
type Preparer struct {
	Tools    Tools
	CacheDir string
	Options  PlanOptions
}

// CleanPartials deletes unfinished outputs (*.part) left in the cache by an
// interrupted run. Call once at startup, before any preparation.
func CleanPartials(cacheDir string) (removed int) {
	_ = filepath.WalkDir(cacheDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".part") && os.Remove(p) == nil {
			removed++
		}
		return nil
	})
	return removed
}

// DetectEncoder returns NVENC when a working NVIDIA encoder is present,
// otherwise libx264. It runs a tiny real encode, because a listed encoder
// can still fail at runtime (no GPU, old driver).
func DetectEncoder(ctx context.Context, ffmpeg string) Encoder {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-v", "error", "-f", "lavfi",
		"-i", "color=black:s=256x256:d=0.2", "-c:v", "h264_nvenc", "-f", "null", "-")
	procgroup.Hide(cmd)
	if cmd.Run() == nil {
		return EncoderNVENC
	}
	return EncoderX264
}

// Prepare probes and plans src, then produces its variants. Lossless and
// direct variants are always produced; the slow compatibility encode only if
// withCompat is true (or it is already cached). onItem is called whenever a
// new variant becomes playable, so viewers can start before slower ones finish.
func (pr *Preparer) Prepare(ctx context.Context, src string, withCompat bool,
	onProgress func(Progress), onItem func(*Item)) (*Result, error) {
	onProgress(Progress{Step: "Reading file", ETA: -1})
	st, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	probe, err := RunProbe(ctx, pr.Tools.FFprobe, src)
	if err != nil {
		return nil, err
	}
	plan, err := MakePlan(probe, pr.Options)
	if err != nil {
		return nil, err
	}

	id := itemID(src, st.Size(), st.ModTime().UnixNano(), plan.Audio.Index, pr.Options.Encoder)
	dir := pr.ItemDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	res := &Result{Item: &Item{ID: id, Title: titleFrom(src), Duration: probe.Duration()}}
	item := res.Item

	onProgress(Progress{Step: "Loading subtitles", ETA: -1})
	item.Subs = append(sidecarSubs(src), pr.embeddedSubs(ctx, src, plan.Subs)...)
	item.Subs = append(item.Subs, LoadUploads(dir)...)
	item.ImageSubs = plan.ImageSubs

	// Decide what to build: everything cached or cheap, plus compat on request.
	type task struct {
		job    Job
		out    string
		cached bool
	}
	var tasks []task
	for _, j := range plan.Jobs {
		out := filepath.Join(dir, j.Variant.ID+".mp4")
		_, statErr := os.Stat(out)
		t := task{job: j, out: out, cached: statErr == nil}
		if !j.Direct && !j.Variant.Lossless && !t.cached && !withCompat {
			res.Pending = append(res.Pending, j.Variant)
			continue
		}
		tasks = append(tasks, t)
	}
	steps := 0
	for _, t := range tasks {
		if !t.job.Direct && !t.cached {
			steps++
		}
	}

	step := 0
	for _, t := range tasks {
		v := t.job.Variant
		if t.job.Direct {
			v.Path = src
		} else {
			if !t.cached {
				step++
				label := "Preparing " + v.Label
				started := time.Now()
				err := pr.run(ctx, t.job.Args, src, t.out, item.Duration, func(f float64) {
					p := Progress{Step: label, StepIndex: step, StepCount: steps, Fraction: f, ETA: -1}
					if el := time.Since(started).Seconds(); f > 0.01 && el > 3 {
						p.ETA = el * (1 - f) / f
					}
					if f >= 0.999 {
						p.Step, p.ETA = label+" · finishing", -1 // moving the index to the front
					}
					onProgress(p)
				})
				if err != nil {
					return nil, fmt.Errorf("%s: %w", v.Label, err)
				}
			}
			v.Path = t.out
			if fi, err := os.Stat(t.out); err == nil && item.Duration > 0 {
				v.Bitrate = int64(float64(fi.Size()*8) / item.Duration)
			}
		}
		item.Variants = append(item.Variants, v)
		snapshot := *item
		snapshot.Variants = append([]Variant(nil), item.Variants...)
		onItem(&snapshot)
	}
	onProgress(Progress{Step: "Ready", StepIndex: steps, StepCount: steps, Fraction: 1})
	return res, nil
}

// run executes ffmpeg, writing to a temp file that is renamed on success so
// a crash never leaves a truncated file in the cache.
func (pr *Preparer) run(ctx context.Context, tmpl []string, in, out string, duration float64, progress func(float64)) error {
	part := out + ".part"
	args := []string{"-hide_banner", "-v", "error"}
	for _, a := range tmpl {
		switch a {
		case "{in}":
			a = in
		case "{out}":
			a = part
		}
		args = append(args, a)
	}
	cmd := exec.CommandContext(ctx, pr.Tools.FFmpeg, args...)
	procgroup.Hide(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 8 << 10}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || (k != "out_time_us" && k != "out_time_ms") || duration <= 0 {
			continue
		}
		if us, err := strconv.ParseFloat(v, 64); err == nil {
			progress(min(1, us/1e6/duration))
		}
	}
	if err := cmd.Wait(); err != nil {
		_ = os.Remove(part)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 && len(msg) > 300 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("ffmpeg failed: %s", msg)
	}
	return os.Rename(part, out)
}

func (pr *Preparer) embeddedSubs(ctx context.Context, src string, streams []ProbeStream) []Subtitle {
	var out []Subtitle
	for _, s := range streams {
		cmd := exec.CommandContext(ctx, pr.Tools.FFmpeg, "-hide_banner", "-v", "error", "-i", src,
			"-map", fmt.Sprintf("0:%d", s.Index), "-f", "webvtt", "pipe:1")
		procgroup.Hide(cmd)
		vtt, err := cmd.Output()
		if err != nil || len(vtt) < 10 {
			continue
		}
		label := embeddedLabel(s)
		out = append(out, Subtitle{ID: fmt.Sprintf("e%d", s.Index), Lang: s.Lang(), Label: label, VTT: vtt})
	}
	return out
}

// embeddedLabel names a track by language plus its role. The raw title tag
// is ignored: release groups fill it with noise like "ITA FORCED - srt".
func embeddedLabel(s ProbeStream) string {
	label := langName(s.Lang())
	title := strings.ToLower(s.Tags["title"])
	switch {
	case s.Disposition["forced"] == 1 || strings.Contains(title, "forced"):
		label += " · forced"
	case s.Disposition["hearing_impaired"] == 1 || strings.Contains(title, "sdh"):
		label += " · SDH"
	case strings.Contains(title, "commentary"):
		label += " · commentary"
	}
	return label
}

// sidecarSubs finds "<name>*.srt|.vtt" next to the video.
func sidecarSubs(src string) []Subtitle {
	dir := filepath.Dir(src)
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Subtitle
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if e.IsDir() || (ext != ".srt" && ext != ".vtt") {
			continue
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if !strings.HasPrefix(strings.ToLower(stem), strings.ToLower(base)) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(data) > MaxSubtitleSize {
			continue
		}
		lang := GuessLang(stem[len(base):] + ext) // only the suffix: "The.Italian.Job.srt" is not Italian
		if ext == ".srt" {
			data = subs.SRTToVTT(data)
		}
		label := langName(lang)
		if lang == "" {
			label = "Subtitles"
		}
		out = append(out, Subtitle{ID: fmt.Sprintf("s%d", len(out)), Lang: lang, Label: label, VTT: data})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func langName(code string) string {
	names := map[string]string{
		"eng": "English", "en": "English", "ita": "Italiano", "it": "Italiano", "por": "Português", "pt": "Português",
		"spa": "Español", "es": "Español", "fre": "Français", "fra": "Français", "fr": "Français",
		"ger": "Deutsch", "deu": "Deutsch", "de": "Deutsch",
	}
	if n, ok := names[code]; ok {
		return n
	}
	if code == "" {
		return "Unknown"
	}
	return strings.ToUpper(code)
}

func titleFrom(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name = strings.NewReplacer(".", " ", "_", " ", "(", " ", ")", " ", "[", " ", "]", " ").Replace(name)
	// Cut release noise after a year: "The Shining 1980 MULTi UHD …" → "The Shining (1980)".
	f := strings.Fields(name)
	for i, w := range f {
		if len(w) == 4 && (strings.HasPrefix(w, "19") || strings.HasPrefix(w, "20")) && i > 0 {
			if _, err := strconv.Atoi(w); err == nil {
				return strings.Join(f[:i], " ") + " (" + w + ")"
			}
		}
	}
	return strings.Join(f, " ")
}

func itemID(path string, size, mtime int64, audio int, enc Encoder) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("v1|%s|%d|%d|%d|%s", path, size, mtime, audio, enc)))
	return hex.EncodeToString(h[:8])
}

// limitedWriter keeps the first n bytes: a broken file can make ffmpeg log
// without end.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, _ = l.w.Write(q)
	return len(p), nil
}
