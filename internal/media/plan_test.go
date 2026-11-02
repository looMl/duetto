package media

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theShining mirrors ffprobe output for the user's UHD remux (trimmed).
const theShining = `{
 "format": {"format_name": "matroska,webm", "duration": "8625.760000", "bit_rate": "22298797"},
 "streams": [
  {"index":0,"codec_name":"hevc","codec_type":"video","profile":"Main 10","level":153,"width":3840,"height":2160,
   "pix_fmt":"yuv420p10le","color_transfer":"smpte2084","color_primaries":"bt2020","tags":{"language":"eng"}},
  {"index":1,"codec_name":"dts","codec_type":"audio","profile":"DTS-HD MA","channels":6,"disposition":{"default":1},"tags":{"language":"eng"}},
  {"index":2,"codec_name":"ac3","codec_type":"audio","channels":6,"tags":{"language":"fre"}},
  {"index":4,"codec_name":"ac3","codec_type":"audio","channels":2,"tags":{"language":"ita"}},
  {"index":7,"codec_name":"hdmv_pgs_subtitle","codec_type":"subtitle","tags":{"language":"eng"}}
 ]}`

func probeFrom(t *testing.T, s string) *Probe {
	t.Helper()
	var p Probe
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestPlanUHDHDR(t *testing.T) {
	p := probeFrom(t, theShining)
	plan, err := MakePlan(p, PlanOptions{Encoder: EncoderNVENC})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Audio.Index != 1 {
		t.Fatalf("expected default English DTS, got %d", plan.Audio.Index)
	}
	if len(plan.Subs) != 0 {
		t.Fatal("PGS must not be planned for extraction")
	}
	if len(plan.Jobs) != 2 {
		t.Fatalf("want passthrough + compat, got %d", len(plan.Jobs))
	}
	orig, compat := plan.Jobs[0], plan.Jobs[1]

	if orig.Direct || !orig.Variant.Lossless || !orig.Variant.HDR {
		t.Fatalf("bad original: %+v", orig.Variant)
	}
	if orig.Variant.Codecs != "hvc1.2.4.L153.B0, mp4a.40.2" {
		t.Fatalf("codecs %q", orig.Variant.Codecs)
	}
	a := strings.Join(orig.Args, " ")
	for _, want := range []string{"-c:v copy", "-tag:v hvc1", "-map 0:1", "-c:a aac", "+faststart"} {
		if !strings.Contains(a, want) {
			t.Errorf("original args missing %q: %s", want, a)
		}
	}

	if compat.Variant.Height != 1080 || compat.Variant.Width != 1920 || compat.Variant.HDR {
		t.Fatalf("bad compat: %+v", compat.Variant)
	}
	c := strings.Join(compat.Args, " ")
	for _, want := range []string{"-hwaccel cuda", "h264_nvenc", "tonemap=tonemap=hable", "zscale=w=-2:h=1080", "format=yuv420p"} {
		if !strings.Contains(c, want) {
			t.Errorf("compat args missing %q: %s", want, c)
		}
	}
}

func TestPlanPreferredAudioLanguage(t *testing.T) {
	plan, err := MakePlan(probeFrom(t, theShining), PlanOptions{AudioLang: "ita", Encoder: EncoderX264})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Audio.Index != 4 {
		t.Fatalf("want Italian track, got %d", plan.Audio.Index)
	}
	if !strings.Contains(strings.Join(plan.Jobs[1].Args, " "), "libx264") {
		t.Fatal("x264 encoder not used")
	}
}

func TestPlanBrowserReadyMP4IsDirect(t *testing.T) {
	p := probeFrom(t, `{"format":{"format_name":"mov,mp4,m4a,3gp,3g2,mj2","duration":"10","bit_rate":"5000000"},
	 "streams":[{"index":0,"codec_name":"h264","codec_type":"video","profile":"High","level":41,"width":1920,"height":1080,"pix_fmt":"yuv420p"},
	            {"index":1,"codec_name":"aac","codec_type":"audio","channels":2}]}`)
	plan, err := MakePlan(p, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || !plan.Jobs[0].Direct {
		t.Fatalf("want a single direct job, got %+v", plan.Jobs)
	}
	if plan.Jobs[0].Variant.Codecs != "avc1.640029, mp4a.40.2" {
		t.Fatalf("codecs %q", plan.Jobs[0].Variant.Codecs)
	}
}

func TestPlanH264MKVWithAC3IsRemuxedLosslessly(t *testing.T) {
	p := probeFrom(t, `{"format":{"format_name":"matroska,webm","duration":"10"},
	 "streams":[{"index":0,"codec_name":"h264","codec_type":"video","profile":"High","level":40,"width":1920,"height":800,"pix_fmt":"yuv420p"},
	            {"index":1,"codec_name":"ac3","codec_type":"audio","channels":6},
	            {"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"ita"}}]}`)
	plan, err := MakePlan(p, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Direct || !plan.Jobs[0].Variant.Lossless {
		t.Fatalf("want one lossless remux job, got %+v", plan.Jobs)
	}
	if len(plan.Subs) != 1 || plan.Subs[0].Lang() != "ita" {
		t.Fatalf("subrip track not planned: %+v", plan.Subs)
	}
}

func TestPlanHi10PNeedsEncode(t *testing.T) {
	p := probeFrom(t, `{"format":{"format_name":"matroska,webm","duration":"10"},
	 "streams":[{"index":0,"codec_name":"h264","codec_type":"video","profile":"High 10","width":1280,"height":720,"pix_fmt":"yuv420p10le"},
	            {"index":1,"codec_name":"aac","codec_type":"audio"}]}`)
	plan, err := MakePlan(p, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].Variant.ID != "compat" {
		t.Fatalf("want only compat job, got %+v", plan.Jobs)
	}
	if strings.Contains(strings.Join(plan.Jobs[0].Args, " "), "scale=") {
		t.Fatal("720p source must not be rescaled")
	}
}

func TestResLabelUsesWidthForScopeFilms(t *testing.T) {
	cases := map[[2]int]string{{1920, 800}: "1080p", {3840, 1600}: "4K", {1280, 536}: "720p", {720, 480}: "480p"}
	for in, want := range cases {
		if got := resLabel(in[0], in[1]); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
}

func TestItemJSONHasNoNullArrays(t *testing.T) {
	b, err := json.Marshal(&Item{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"variants":[]`) || !strings.Contains(s, `"subs":[]`) {
		t.Fatalf("null arrays crash the player: %s", s)
	}
}

func TestEmbeddedLabelAndImageSubCount(t *testing.T) {
	forced := ProbeStream{CodecName: "subrip", Tags: map[string]string{"language": "ita", "title": "ITA FORCED - srt"},
		Disposition: map[string]int{"forced": 1}}
	if got := embeddedLabel(forced); got != "Italiano · forced" {
		t.Fatalf("label %q", got)
	}
	p := probeFrom(t, theShining)
	plan, _ := MakePlan(p, PlanOptions{})
	if plan.ImageSubs != 1 {
		t.Fatalf("image subs %d", plan.ImageSubs)
	}
}

func TestTitleFrom(t *testing.T) {
	cases := map[string]string{
		"/x/The.Shining.1980.MULTi.UHD.Blu-ray.2160p.mkv":                                "The Shining (1980)",
		"The Shining - Remastered (1980) (1080p x265 BDRip ITA ENG MULTISUB) - GEGE.mkv": "The Shining - Remastered (1980)",
		"Arrival [2016] 1080p.mkv":                                                       "Arrival (2016)",
		"1917.mkv":                                                                       "1917",
	}
	for in, want := range cases {
		if got := titleFrom(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestSidecarLanguageComesFromSuffixOnly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "The.Italian.Job.2003.mkv")
	for _, n := range []string{"The.Italian.Job.2003.srt", "The.Italian.Job.2003.eng.srt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]bool{}
	for _, s := range sidecarSubs(src) {
		got[s.Label] = true
	}
	if !got["Subtitles"] || !got["English"] || len(got) != 2 {
		t.Fatalf("labels %v", got)
	}
}
