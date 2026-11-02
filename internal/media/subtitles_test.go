package media

import (
	"context"
	"strings"
	"testing"
)

func TestGuessLang(t *testing.T) {
	cases := map[string]string{
		"The.Shining.1980.ita.srt":         "ita",
		"The Shining [English].srt":        "eng",
		"shining.pt.srt":                   "por",
		"It.2017.1080p.srt":                "",
		"La.Casa.de.Papel.S01E01.srt":      "",
		"The.Shining.1980.1080p.srt":       "",
		"Shining.ENG.SDH.forced.ita.srt":   "ita",
		"Il Mondo Italiano Del Cinema.srt": "ita", // last matching token wins
	}
	for name, want := range cases {
		if got := GuessLang(name); got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}

func TestToVTT(t *testing.T) {
	ctx := context.Background()
	srt := []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\nCiao\r\n")
	vtt, err := ToVTT(ctx, "", "a.SRT", srt)
	if err != nil || !strings.Contains(string(vtt), "00:00:01.000 --> 00:00:02.000") {
		t.Fatalf("srt: %v %q", err, vtt)
	}
	if _, err := ToVTT(ctx, "", "a.vtt", []byte("\xEF\xBB\xBFWEBVTT\n\n00:01.000 --> 00:02.000\nhi\n")); err != nil {
		t.Fatalf("vtt with BOM rejected: %v", err)
	}
	if _, err := ToVTT(ctx, "", "a.vtt", []byte("nope")); err == nil {
		t.Fatal("headerless vtt accepted")
	}
	if _, err := ToVTT(ctx, "", "a.srt", []byte("not subtitles at all")); err == nil {
		t.Fatal("empty srt accepted")
	}
	if _, err := ToVTT(ctx, "", "a.txt", srt); err == nil {
		t.Fatal("unknown extension accepted")
	}
}

func TestUploadsPersistInOrder(t *testing.T) {
	dir := t.TempDir()
	var all []Subtitle
	for i, lang := range []string{"ita", "ita", ""} {
		s := Subtitle{ID: NextUploadID(all), Lang: lang, Label: UploadLabel(lang, all), VTT: []byte("WEBVTT\n")}
		if err := SaveUpload(dir, s); err != nil {
			t.Fatal(err)
		}
		all = append(all, s)
		_ = i
	}
	got := LoadUploads(dir)
	labels := []string{}
	for _, s := range got {
		labels = append(labels, s.ID+"="+s.Label)
	}
	if strings.Join(labels, ",") != "u1=Italiano,u2=Italiano (2),u3=Uploaded" {
		t.Fatalf("got %v", labels)
	}
}
