# Contributing

## Setup

Use Go 1.26.11 or newer (the `go` line in `go.mod`) and FFmpeg/FFprobe for
playback. The UI is native JavaScript modules: no package install, no build
step. Format Go code with `gofmt`.

## Before opening a pull request

```sh
go test -race ./...
go vet ./...
node --experimental-vm-modules --test web/tests/*.test.mjs
python3 scripts/check-publication.py
```

CI runs these plus secret and vulnerability scans on every pull request.

- Describe the problem, the new behavior and how you checked it.
- Add regression tests for changes to authentication, authorization, file
  handling or concurrency.
- Try UI and playback changes in a browser.

## Privacy

Use synthetic usernames, media paths and fixtures, and keep data directories,
credentials, logs and personal media out of commits, screenshots and issues.
Commits use the neutral `Lex contributors` identity; if the publication check
flags yours, set it for this checkout only (`git config user.name` /
`user.email`), not globally. See the [publication audit](docs/PUBLISH_AUDIT.md)
for what the check covers.

## Code layout

```text
cmd/lex            flags, wiring, periodic scans
internal/store     SQLite schema and queries
internal/library   scanner, filename parser, ffprobe
internal/meta      TMDB, TVmaze, Radarr, artwork and metadata
internal/stream    playback decisions, ffmpeg jobs, sessions, subtitles
internal/subsearch OpenSubtitles.com search and download
internal/trickplay seek-bar preview thumbnails
internal/cache     optional media disk cache
internal/intro     optional intro & end-credits detection
internal/tv        Live TV channels and their daily schedules
internal/sysstats  host/container statistics
internal/api       HTTP API, auth, images, static files
internal/logx      leveled logger with a recent-lines buffer for the UI
internal/proc      child-process helpers
internal/version   version number and build commit
web/static         vanilla JavaScript UI (no build step)
web/static/vendor  prebuilt third-party browser code (JASSUB subtitle renderer, FFmpeg audio converter)
wasm/audio         source of the browser's AC-3/E-AC-3/DTS/TrueHD → AAC converter (scripts/build-audio-wasm.sh)
wasm/remux         source of the browser's MKV/WebM → fragmented MP4 remuxer (scripts/build-remux-wasm.sh)
deploy             Linux deployment examples
docker             Raspberry Pi archive keys for the container build
scripts            release, WebAssembly builds and publication privacy checks
```

## License

Lex is licensed under the [MIT License](LICENSE). By contributing, you agree
that your contributions are licensed under the same terms.
