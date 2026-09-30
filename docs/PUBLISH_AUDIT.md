# Publication audit

Audit date: 2026-09-30. Scope: tracked/unignored source, deployment examples,
Go dependencies and toolchain, HTTP authentication/authorization, playback
sessions, filesystem disclosure, artwork fetching, browser UI, and unpublished
Git history. This is an AI-assisted code review with automated and synthetic
smoke checks, not an independent security assessment.

## Changes made

| Finding | Resolution |
| --- | --- |
| Private deployment address and machine-specific README details | Removed; replaced with portable localhost, Compose and Linux service instructions. |
| Personal attribution and private documentation retained in unpublished history | Replaced publication history with a neutral initial commit; removed old local checkpoint refs and expired reflogs. |
| Original Go 1.26.0 scan reported 19 reachable standard-library advisories | Raised minimum Go version and Docker builder to 1.26.8; final reachable-vulnerability scan passes. |
| Concurrent first-run requests could create multiple admins | Single conditional SQLite insert; concurrent regression test. |
| Four-character passwords and non-transactional session revocation | New/changed passwords require 12–72 bytes; password replacement and token revocation use one transaction. Existing passwords still authenticate. |
| Concurrent demotions/deletions could remove every admin | Atomic conditional writes preserve the last administrator. |
| Private peers broadly trusted; HTTPS forwarding header trusted unconditionally | Proxy trust defaults off; explicit proxy CIDR allowlist applies to both client IP and HTTPS headers. |
| Cross-site browser mutations lacked an explicit guard | Go cross-origin protection covers the HTTP router; added CSP and existing response-header checks. |
| API session tokens accepted through URL parameters | Removed URL authentication; cookies and explicit headers supported. POST-only scan webhook retains its distinct scan token. |
| Library and playback responses leaked server paths to ordinary users | Redacted library paths, media paths and probe errors; media processing failures direct users to admin logs. |
| Client-supplied playback IDs could replace another user's session | Enforced session ownership and item/file checks. |
| Concurrent FFmpeg launches could exceed the transcode limit; full transcode slots blocked remux | Serialized session job replacement and atomic limit/registration; remux has separate admission behavior. |
| Session state escaped its lock through reads and heartbeats | Return copied state for API reads and capture ending state under the lock. |
| Provider errors could expose credential-bearing URLs | Strip userinfo, query and fragment from reported URLs; avoid echoing provider error bodies or transport URLs. |
| Artwork URLs could reach private services; image processing had weak bounds | Validate resolved public IPs at connection time, including redirects; reject oversized downloads and cap resize dimensions. |
| Concurrent image resizing shared a temporary filename | Unique temporary files with cleanup and atomic rename. |
| Database files could be group/world-readable and special path characters broke the DSN | Private new data-directory/database permissions and escaped SQLite file URLs. |
| Login-failure map could grow while all entries were live | Bounded eviction; regression test. |
| Network exposure and destination-specific service defaults | Loopback defaults, portable `lex` user/data paths, named Compose data volume, explicit SSH target/argument validation. |
| Predictable root-owned staging files under `/tmp` | Private `mktemp` staging directory and validated path in binary deployment. |
| Missing publication checks and disclosure | CI, privacy guard, security/contribution docs, updated README and AI disclosure in README and Settings → About. |

The initial Gitleaks report matched a SQL upsert expression in the media cache;
it was inspected as a false positive and reformatted. No secret-scanner rules
were suppressed. Bundled Raspberry Pi archive keys were identified as public
OpenPGP verification certificates, not private keys.

## Validation

- `go test -race ./...` and `go vet ./...` passed using Go 1.26.8.
- Formatting, module consistency, JavaScript module syntax, shell syntax and
  both Compose configurations passed.
- Linux amd64 and arm64 static binaries built successfully with `-trimpath`.
- Generic arm64 Debian/FFmpeg and Raspberry Pi archive container variants built.
  The generic image started as UID 1000, passed `lex -healthcheck`, and created a
  mode-0600 database. Named-volume Compose startup was checked separately.
- Synthetic 12-second H.264/AAC media was scanned/probed. Direct play, remux and
  software transcode plans each returned HTTP 200 and non-empty MP4 responses.
  Direct HTTP ranges and the browser player were exercised.
- Browser setup, library/item pages, server settings, AI disclosure, direct play
  and MSE playback were checked with synthetic data. No application errors or
  CSP violations were observed in those checks. The preview snapshot endpoint
  failed; DOM inspection and browser interactions were used, so screenshot
  layout review was not completed.
- Gitleaks source/history scans and the targeted publication privacy guard
  passed after history replacement. No real credentials were found.
- `govulncheck` reported zero reachable or imported-package vulnerabilities.
  It still reports module advisory
  [GO-2026-5932](https://pkg.go.dev/vuln/GO-2026-5932) for the unused, unmaintained
  `golang.org/x/crypto/openpgp` component. Lex imports bcrypt from that module,
  not OpenPGP; the scanner finds no call path to that advisory.

## Limits and publication status

- Raspberry Pi hardware decoding/encoding and destination SSH/systemd/Quadlet
  deployment were not exercised on a physical board/remote host. Device IDs,
  group IDs and paths remain operator-specific examples.
- Metadata-provider integration was reviewed but not tested with real API keys.
  Load/soak testing, codec/subtitle/HDR matrices, and full mobile/accessibility
  coverage remain outside this audit.
- Administrators are trusted with filesystem access and every user's account.
  All signed-in users can play all libraries. Runtime databases, logs and
  backups deliberately contain account/device/playback information; protect
  them and keep them out of publication.
- The privacy guard detects specific patterns and file types. It does not prove
  the absence of every possible form of PII. Future screenshots, media fixtures
  and issue reports still need review.
- The repository is licensed under the MIT License (`LICENSE`); bundled
  third-party components are listed in `THIRD_PARTY_NOTICES.md`.
- Push only the cleaned `main` branch; avoid mirror pushes of application refs.

Scanner reference: [Go vulnerability management](https://go.dev/doc/security/vuln/).
