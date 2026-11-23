# Maintaining lex

Notes for the maintainer: releases, CI details and manual stress testing.
Contributors only need [contributing](../CONTRIBUTING.md).

## Releases

The version number lives in `internal/version/VERSION`; builds add the commit
they came from (`lex -version` prints e.g. `lex 0.0.31 (1507120)`, also shown
in Settings → About and the account menu). To release, bump that file, commit,
and push a matching tag:

```sh
git tag -a v0.0.31 -m "lex v0.0.31" && git push origin v0.0.31
```

The container image workflow checks the tag against the file, publishes the
arm64 image and creates a GitHub Release (notes only).

## CI

CI runs on pull requests only (merging doesn't run it again; PRs that only
change docs or the version number skip it). It checks formatting, module
consistency, race tests, `go vet`, JavaScript/shell syntax, Compose
configuration, publication privacy, secrets and vulnerabilities, as parallel
jobs. PRs that change the Dockerfile or its inputs also build the arm64 image.
To run the extra scans locally:

```sh
go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --no-banner --log-opts="HEAD" .
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
```

The publication script checks working files and branch/tag history for private
deployment addresses, home paths, runtime/configuration files and personal
commit attribution; `--since origin/main` checks only the branch's commits, as
CI does. Commit emails must be a GitHub no-reply address (or the old neutral
`Lex contributors` one); any name is fine. It complements manual review and cannot
detect every form of PII. The [publication audit](PUBLISH_AUDIT.md) records the
pre-publication review.

Never `git push --mirror`: application checkpoint refs are local metadata.

## Playback stress test

Copy `web/tests/browser-stress.html` and `web/tests/browser-stress.js` to
`web/static/stress.html` and `web/static/stress.js` in a temporary checkout and
rebuild. In a signed-in Firefox tab, open
`/stress.html?items=<episode-id>,<movie-id>,<movie-id>&minutes=20`. It checks
seeks, pause/resume, network failures, buffer pressure, a real server restart
(restart the server when the page says `RESTART SERVER NOW`) and continuous
playback, then downloads a JSON result, restores preferences and suppresses
watch progress. Add `restartOnly` to repeat the restart, movie and continuous
checks, or `continuousOnly` for just the endurance check. Remove the temporary
files before building a release.
