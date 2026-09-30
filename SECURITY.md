# Security and privacy

Lex is intended for a trusted, self-hosted media library. Administrators can
browse the server filesystem, choose library/cache paths, manage every account,
and see playback history and logs. All signed-in users can play all libraries;
there are no per-library permissions.

## Deployment

- Complete first-admin setup through localhost or a trusted private connection
  before exposing the server. The first successful setup request owns the server.
- Use HTTPS through a reverse proxy or a private VPN. The server itself speaks
  HTTP. Compose and the example Linux services bind to loopback by default.
- Proxy-header trust is disabled for new installations. If enabled, set
  `trustedProxies` to the proxy peer's exact CIDRs. The proxy must replace incoming
  forwarding headers, preserve the public `Host`, and block direct access to Lex.
  Existing installations keep their saved toggle but receive a loopback allowlist.
- Browser mutations use Go's cross-origin protection. API clients without browser
  origin headers can use cookies, `Authorization: Bearer`, or `X-Lex-Token`.
  Authentication tokens in query strings are rejected.
- New and changed passwords must be 12–72 bytes. Existing password hashes still
  work. Password changes revoke existing sessions. Session tokens expire after
  90 days of inactivity; sign out unused devices in Settings.
- The scan webhook accepts POST with its separate `token` query parameter.
  Treat its URL as a secret and redact query strings in reverse-proxy logs.
- Only use trusted media and metadata sidecars. Keep the OS and FFmpeg patched.
  Media probing and transcoding run subprocesses, and transcoding can exhaust
  resources. The concurrent transcode setting is a resource limit, not a quota
  or a complete denial-of-service defense.

## Stored data

The data directory contains usernames, password hashes, API credentials,
authentication tokens, media paths, watch state, artwork and subtitle caches.
Device records and playback history include client IP addresses and browser
labels. Admin logs can include usernames, IPs, titles and paths. Lex creates new
data directories with mode 0700 and databases with mode 0600. Protect existing
folder permissions, backups and custom cache directories as well.

Metadata lookups send title/year or provider IDs to the configured provider.
Artwork is downloaded from public HTTP(S) addresses; private, loopback and
link-local addresses are blocked at connection time, including after redirects.
Radarr API access intentionally supports a local/private endpoint configured by
an administrator. Disable online metadata providers if these requests are unwanted.
There is no bundled analytics or advertising service. Account/watch data stays
in the local database unless an operator exports it.

Never include databases, real credentials, personal deployment URLs, account
exports, logs, or screenshots of private libraries in issues or commits. Use
synthetic examples. The included Raspberry Pi PGP files are public upstream
archive verification keys, not private signing keys.

## Reporting a vulnerability

When this repository is hosted on GitHub, use its private vulnerability reporting
feature if the maintainer has enabled it. Otherwise contact the maintainer through
an available private channel before publishing exploit details. Include affected
versions, impact and a minimal reproduction with synthetic data.
