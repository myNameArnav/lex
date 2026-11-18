# Contributing

Use Go 1.26.11 or newer (the `go` line in `go.mod`) and format changes with
`gofmt`. FFmpeg and FFprobe are needed to exercise actual playback. The UI
consists of native JavaScript modules with no package installation or build
step.

Run these checks before opening a pull request:

```sh
go test -race ./...
go vet ./...
node --experimental-vm-modules --test web/tests/*.test.mjs
python3 scripts/check-publication.py
go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --no-banner --log-opts="HEAD" .
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

Use synthetic usernames, media paths and fixtures. Keep data directories,
credentials and logs out of Git. This repository uses neutral commit attribution
to keep personal information out of public history. If the publication privacy
check asks for it, set that identity locally for this checkout; do not change
global Git identity settings.

Describe the problem, resulting behavior and checks in pull requests. Include
regression tests for authentication, authorization, file handling and concurrency
changes. Check the browser when changing UI or playback behavior.

For manual playback stress testing, copy `web/tests/browser-stress.html` and
`web/tests/browser-stress.js` to `web/static/stress.html` and `web/static/stress.js`
in a temporary checkout, then rebuild the server. In a signed-in Firefox tab,
open `/stress.html?items=<episode-id>,<movie-id>,<movie-id>&minutes=20`. The
fixture checks seeks, pause/resume, network failures, buffer pressure, a real
server restart and continuous playback. Restart the server when the page says
`RESTART SERVER NOW`. It downloads a JSON result, restores preferences and
suppresses watch progress. Use `restartOnly` to repeat the restart, movie and
continuous checks, or `continuousOnly` for just the endurance check. Remove the
temporary static files before building the normal release.

## Checks and publication

CI runs on pull requests (merging doesn't run it again, and PRs that only change
docs or the version number skip it) and checks formatting, module consistency, race tests, Go vet, JavaScript/shell
syntax, Compose configuration, publication privacy, secrets and vulnerabilities.
Pull requests that change the Dockerfile or its inputs also build the arm64 image. The privacy script checks working
files and branch/tag history for private deployment addresses, home paths,
runtime/configuration files and personal commit attribution; with
`--since origin/main` it checks only your branch's commits, as CI does for a
pull request (CI's test, lint and security checks run as parallel jobs). It complements
manual review and secret scanning; it cannot detect every form of PII.

Before a first GitHub publication, use a fresh repository, run the checks, and
push only the cleaned `main` branch. For example, after choosing a repository
name and signing in to GitHub CLI:

```sh
gh repo create REPOSITORY_NAME --public --source=. --remote=origin --push
```

Do not use `git push --mirror`: application checkpoint refs are local metadata.
Commits use the neutral "Lex contributors" identity; pull requests merged on
GitHub record the maintainer's GitHub no-reply address, which the check allows. Keep
personal data, logs, media and credentials out of future commits, screenshots
and issues. See the
[publication audit](docs/PUBLISH_AUDIT.md) for validation and remaining limits.

## Releases

The version number lives in `internal/version/VERSION`; builds add the commit
they came from (`lex -version` prints e.g. `lex 0.0.20 (2b8f74d)`, and it's shown
in Settings → About and the account menu). To release, bump that file, commit,
and push a matching tag:

```sh
git tag -a v0.0.20 -m "Lex v0.0.20" && git push origin v0.0.20
```

The Container image workflow checks the tag against the file, publishes the
arm64 image and creates a GitHub Release (notes only).

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
