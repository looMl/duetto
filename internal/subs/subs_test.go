package subs

import (
	"strings"
	"testing"
)

func TestSRTToVTT(t *testing.T) {
	in := "\xEF\xBB\xBF1\r\n00:03:12,109 --> 00:03:14,444\r\nHi. I've got an appointment\r\nwith Mr. Ullman.\r\n\r\n2\r\n0:03:14,6 --> 00:03:16,613 X1:0\r\nA --> B\r\n\r\ngarbage\r\n"
	got := string(SRTToVTT([]byte(in)))
	want := "WEBVTT\n\n" +
		"00:03:12.109 --> 00:03:14.444\nHi. I've got an appointment\nwith Mr. Ullman.\n\n" +
		"00:03:14.600 --> 00:03:16.613\nA -> B\n\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestWindows1252Fallback(t *testing.T) {
	in := []byte("1\n00:00:01,000 --> 00:00:02,000\nPerch\xe9 \x93s\xec\x94\n")
	got := string(SRTToVTT(in))
	if !strings.Contains(got, "Perché “sì”") {
		t.Fatalf("bad decode: %q", got)
	}
}
