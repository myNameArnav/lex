# Contributing

Use Go 1.26.8 or newer and format changes with `gofmt`. FFmpeg and FFprobe are
needed to exercise actual playback. The UI consists of native JavaScript modules
with no package installation or build step.

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
(`Lex contributors <contributors@example.invalid>`) to keep personal information
out of public history. Set those values locally for this checkout if required by
the publication privacy check; do not change global Git identity settings.

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

Lex is licensed under the [MIT License](LICENSE). By contributing, you agree
that your contributions are licensed under the same terms.
