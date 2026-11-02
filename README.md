# Duetto

**Watch a movie from your PC together with someone far away: in sync, at full quality, with nothing to install on their side.**

You run one program. They open one link. Either of you can pause, seek or change subtitles, and both screens follow.

---

## Why this exists

My girlfriend and I live in different countries. Movie night used to mean Discord screen share: 720p, smeared dark scenes, and audio that drifted out of sync whenever the connection hiccuped.

The usual fixes didn't fit:

- **Screen sharing and watch-party sites** re-encode your screen or file live, in the browser. Whatever you have, they get a blurry copy.
- **Jellyfin or Plex** can send the real file, but they want accounts, logins and an open port on your router.
- **Sending the file** doesn't work when it's very big and who wants to do that.

So I built the thing I wanted: a tiny server that sends **the actual video stream**, bit for bit, keeps two browsers within a few milliseconds of each other, and hides all the networking behind one link.

## What it does

- **Original quality.** Your guest's browser plays the source video stream itself, never a re-encode. A 4K HDR remux stays 4K HDR when their device can decode it.
- **Automatic fallback.** If a browser can't decode the original (say HEVC in Firefox), the host can build a 1080p H.264 copy on the GPU, with proper HDR→SDR tone mapping. That viewer switches to it automatically.
- **Tight sync.** The server holds the clock. Small drift is fixed by nudging playback speed by up to 5%, which you can't hear; big jumps are fixed with a seek. Automated two-browser tests measure single-digit milliseconds between viewers.
- **Waits for everyone.** If anyone buffers, everyone pauses ("Waiting for Ana…") and resumes together after a short countdown.
- **One link, no install, no ports.** A Cloudflare quick tunnel gives you a public HTTPS link with no account, no router setup, and it works behind CGNAT.
- **Subtitles.** Embedded text tracks and `.srt` files next to the movie are picked up. The host can upload `.srt`, `.vtt` or `.ass` during the session, and everyone gets it.
- **Private by default.** The invite key lives in the link's `#fragment`, so it never reaches a server log. Host controls only exist on a local-only port.

## How it works

```
 Your PC                                                      Internet
┌───────────────────────────────────────────────────────┐
│ duetto.exe (Go, single binary, web UI embedded)       │
│                                                       │
│  127.0.0.1:7790  host page: pick movie, copy link     │
│                                                       │
│  127.0.0.1:7791  ◄── cloudflared ◄────────────────────┼──── HTTPS ──── guest browser
│    /media   prepared MP4, HTTP range requests         │
│    /ws      sync channel (WebSocket)                  │
│    /subs    WebVTT                                    │
│                                                       │
│  ffmpeg: remux (lossless) or encode (fallback)        │
└───────────────────────────────────────────────────────┘
```

**Lossless first.** When you pick a movie, Duetto checks it with `ffprobe` and makes the cheapest browser-playable version:

| Source | What your guest receives |
|---|---|
| H.264 MP4 with AAC | the file itself |
| H.264 or HEVC in MKV, with AC-3, DTS or TrueHD audio | the same video stream, bit for bit, in MP4; audio → AAC |
| HEVC / 4K / HDR on a device that can't play it | a 1080p H.264 SDR copy, built on request (NVENC when available) |

The remux is limited by disk speed, about 1–3 minutes even for a 20 GB file.

**Sync.** Clients estimate the server clock NTP-style over the WebSocket. Playback state is `{paused, position, at}`, and every client computes where it should be right now. "Play" is scheduled 400 ms ahead so everyone starts on the same instant.

## Quick start (Windows)

1. Build `duetto.exe` (see [Building](#building)).
2. Put `ffmpeg.exe` and `ffprobe.exe` in a `tools` folder next to it. Use a **full** build, such as [gyan.dev](https://www.gyan.dev/ffmpeg/builds/); HDR tone mapping needs zimg. `cloudflared.exe` is downloaded automatically on first run and checked against a pinned SHA-256.
3. Double-click `start.bat`. The host page opens in your browser.
4. Pick a movie in the **Host** panel, click **Copy**, and send the link.

Keyboard: `Space` play/pause · `←/→` ±10 s · `F` full screen · `M` mute · `C` subtitles.

Options:

```
duetto.exe -audio ita               prefer the Italian audio track
duetto.exe -library "D:\Movies"     folders to list (default: Videos, Downloads)
duetto.exe -file "D:\Movies\x.mkv"  open a movie at startup
duetto.exe -no-tunnel               LAN only, prints a local link
```

Prepared files are cached in `%LOCALAPPDATA%\duetto`, along with `duetto.log`. Delete the folder to free space.

## Honest limits

- **Your upload speed is the quality ceiling.** It has to beat the file's bitrate, which the Host panel shows. A 1080p file at ~8 Mb/s is easy; a 4K remux wants 40+ Mb/s up.
- **Picture-based subtitles** (Blu-ray PGS, DVD VobSub) can't be shown in a browser. Upload an `.srt` instead.
- **Quick tunnels** are a free Cloudflare service with no guarantees. Great for a movie night, not for running a service.
- **First launch:** Windows SmartScreen will warn about an unsigned app (*More info → Run anyway*).
- **Built and tested on Windows + Chrome/Edge.** The server also runs on Linux and macOS if you install `cloudflared` yourself.

## Building

Requires Go ≥ 1.24, Node ≥ 22 and ffmpeg on `PATH` for the integration tests.

```sh
cd web && npm ci && npm run build && cd ..          # frontend → web/dist (embedded)
go build -o duetto ./cmd/duetto                    # or: GOOS=windows go build -o duetto.exe ./cmd/duetto
```

Or, on Windows, run `.\build.ps1`: it checks, tests and builds `duetto.exe`.

## Testing

```sh
go test -race ./...                                 # unit + ffmpeg integration tests
cd web && npm run check && npm test && cd ..        # types + sync math
node e2e/sync.cjs ./duetto                          # two browsers: drift, pause, seek, auth
node e2e/fallback.cjs ./duetto                      # undecodable original → compatible copy
node e2e/subtitles.cjs ./duetto                     # subtitle upload, display, persistence
```

The end-to-end scripts drive real browsers with [Playwright](https://playwright.dev) against a real binary.

## Project layout

```
cmd/duetto/       entry point: flags, listeners, shutdown
internal/room/    authoritative playback state and the "wait for everyone" barrier
internal/server/  HTTP + WebSocket: auth, range-served media, admin API
internal/media/   ffprobe → plan → ffmpeg; subtitles; cache
internal/tunnel/  cloudflared download, verification, lifecycle
internal/app/     glue: library, preparation jobs, host actions
web/              Svelte 5 + TypeScript player, embedded into the binary
e2e/              Playwright end-to-end scripts
```

## Lessons learned the hard way

- **A paused `<video>` stops downloading early.** Windows Chrome reads about 2 MB ahead and then idles. Waiting for "3 seconds buffered" before starting meant waiting forever. Trust `readyState` instead.
- **Chrome can play sound over a black screen without reporting an error** when it can't decode the video track. Check `videoWidth`.
- **Go encodes empty slices as `null`.** The player expected a list and crashed on the first movie without subtitles.
- **Changing `cue.line` doesn't move a subtitle that's already on screen** in Chrome. Move the whole cue layer with CSS instead.
- **cloudflared quick tunnels refuse to start** if `~/.cloudflared/config.yml` exists. Pass an empty config.
- **Scope films are 1920×800,** so resolution labels have to come from width, not height.
