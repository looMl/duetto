package media

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Integration tests: run only where ffmpeg/ffprobe are installed.
func tools(t *testing.T) Tools {
	t.Helper()
	ff, err1 := exec.LookPath("ffmpeg")
	fp, err2 := exec.LookPath("ffprobe")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg not installed")
	}
	return Tools{FFmpeg: ff, FFprobe: fp}
}

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-v", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

func prepare(t *testing.T, tl Tools, src string) (*Item, []*Item) {
	t.Helper()
	res, published := prepareWith(t, &Preparer{Tools: tl, CacheDir: t.TempDir(), Options: PlanOptions{Encoder: EncoderX264}}, src, true)
	return res.Item, published
}

func prepareWith(t *testing.T, pr *Preparer, src string, withCompat bool) (*Result, []*Item) {
	t.Helper()
	var published []*Item
	res, err := pr.Prepare(context.Background(), src, withCompat, func(Progress) {}, func(i *Item) { published = append(published, i) })
	if err != nil {
		t.Fatal(err)
	}
	return res, published
}

func TestPrepareH264MKVLosslessRemux(t *testing.T) {
	tl := tools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "Movie.2001.1080p.mkv")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=24:d=3", "-f", "lavfi", "-i", "sine=f=440:d=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-c:a", "ac3", src)
	if err := os.WriteFile(filepath.Join(dir, "Movie.2001.1080p.ita.srt"),
		[]byte("1\n00:00:00,500 --> 00:00:02,000\nCiao\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	it, published := prepare(t, tl, src)
	if it.Title != "Movie (2001)" {
		t.Errorf("title %q", it.Title)
	}
	if len(it.Variants) != 1 || !it.Variants[0].Lossless || len(published) != 1 {
		t.Fatalf("variants %+v", it.Variants)
	}
	if len(it.Subs) != 1 || it.Subs[0].Label != "Italiano" || !strings.Contains(string(it.Subs[0].VTT), "00:00:00.500") {
		t.Fatalf("subs %+v", it.Subs)
	}
	// The video bitstream must be bit-identical to the source.
	if a, b := streamHash(t, src), streamHash(t, it.Variants[0].Path); a != b {
		t.Fatalf("video not lossless: %s vs %s", a, b)
	}
	if p, _ := RunProbe(context.Background(), tl.FFprobe, it.Variants[0].Path); p.Audio()[0].CodecName != "aac" {
		t.Fatal("AC-3 must be converted to AAC")
	}
}

func TestPrepareHDRHEVCMakesTwoVariants(t *testing.T) {
	tl := tools(t)
	src := filepath.Join(t.TempDir(), "hdr.mkv")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=24:d=2", "-f", "lavfi", "-i", "sine=d=2",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=none",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc", "-c:a", "aac", src)

	it, published := prepare(t, tl, src)
	if len(it.Variants) != 2 || len(published) != 2 {
		t.Fatalf("want 2 variants published progressively, got %d/%d", len(it.Variants), len(published))
	}
	if len(published[0].Variants) != 1 || published[0].Variants[0].ID != "original" {
		t.Fatal("the lossless variant must be published first")
	}
	compat := it.Variants[1]
	p, err := RunProbe(context.Background(), tl.FFprobe, compat.Path)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := p.Video()
	if v.CodecName != "h264" || v.PixFmt != "yuv420p" || v.HDR() {
		t.Fatalf("compat not H.264 SDR 8-bit: %+v", v)
	}
	if orig, _ := RunProbe(context.Background(), tl.FFprobe, it.Variants[0].Path); func() string {
		ov, _ := orig.Video()
		return ov.CodecName
	}() != "hevc" {
		t.Fatal("original must stay HEVC")
	}
}

func streamHash(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:v:0", "-c", "copy",
		"-bsf:v", "h264_mp4toannexb", "-f", "hash", "-hash", "sha256", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestCompatIsDeferredUntilRequested(t *testing.T) {
	tl := tools(t)
	src := filepath.Join(t.TempDir(), "hevc.mkv")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=24:d=2", "-f", "lavfi", "-i", "sine=d=2",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=none", "-c:a", "aac", src)
	pr := &Preparer{Tools: tl, CacheDir: t.TempDir(), Options: PlanOptions{Encoder: EncoderX264}}

	res, _ := prepareWith(t, pr, src, false)
	if len(res.Item.Variants) != 1 || len(res.Pending) != 1 || res.Pending[0].ID != "compat" {
		t.Fatalf("want original built and compat pending: %+v / %+v", res.Item.Variants, res.Pending)
	}
	res, _ = prepareWith(t, pr, src, true)
	if len(res.Item.Variants) != 2 || len(res.Pending) != 0 {
		t.Fatalf("compat not built on request: %+v", res.Item.Variants)
	}
	// Once cached, compat is included even when not requested.
	res, _ = prepareWith(t, pr, src, false)
	if len(res.Item.Variants) != 2 {
		t.Fatal("cached compat must be served")
	}
}

func TestCancelLeavesNoPartialFile(t *testing.T) {
	tl := tools(t)
	src := filepath.Join(t.TempDir(), "long.mkv")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=s=1280x720:r=24:d=60", "-f", "lavfi", "-i", "sine=d=60",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "log-level=none", "-c:a", "aac", src)
	cache := t.TempDir()
	pr := &Preparer{Tools: tl, CacheDir: cache, Options: PlanOptions{Encoder: EncoderX264}}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := pr.Prepare(ctx, src, true, func(p Progress) {
		if p.StepIndex == 2 && p.Fraction > 0.05 {
			cancel() // stop during the compat encode
		}
	}, func(*Item) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(cache, "*", "*.part"))
	if len(matches) != 0 {
		t.Fatalf("partial files left: %v", matches)
	}
	if done, _ := filepath.Glob(filepath.Join(cache, "*", "original.mp4")); len(done) != 1 {
		t.Fatal("finished lossless variant must survive a cancel")
	}
}
