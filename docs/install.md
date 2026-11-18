# Installing lex

## Quick start

Build with Go **1.26.11 or newer** and install FFmpeg/FFprobe:

```sh
go build -trimpath -o lex ./cmd/lex
./lex -addr 127.0.0.1:8420 -data ./data
```

Open <http://localhost:8420>, create the first administrator, and add a library
(an absolute path) in Settings → Libraries. lex listens on loopback by default;
use `-addr :8420` for LAN access behind a firewall, and read
[security and privacy](../SECURITY.md) before exposing it further.

| Flag | Environment | Default |
| --- | --- | --- |
| `-addr` | `LEX_ADDR` | `127.0.0.1:8420` |
| `-data` | `LEX_DATA` | `./data` |
| `-ffmpeg` / `-ffprobe` | `LEX_FFMPEG` / `LEX_FFPROBE` | from `PATH` |
| `-debug` | `LEX_DEBUG=1` | off |
| `-version`, `-healthcheck` | — | print version / probe the server and exit |
| `-restore <file>` | — | replace the database with a backup and exit (stop lex first) |

## Docker

A `linux/arm64` image is published to GitHub Container Registry
(`:latest`, `:<major.minor>`, `:<version>`, and `:edge` for manual builds of
`main`). On other platforms, `docker build -t lex .`.

```sh
docker run -d --name lex -p 127.0.0.1:8420:8420 --user 1000:1000 \
  -v lex-data:/data -v /path/to/media:/media:ro ghcr.io/mynamearnav/lex:latest
```

Or edit the media path in `docker-compose.yml` and run `docker compose up -d`.
Add `/media` as the library path in lex. On a Raspberry Pi, add
`-f docker-compose.rpi.yml` to pass the hardware codec devices through; for
other arm64 boards build with `--build-arg RPI_FFMPEG=no`. HEVC hardware
decoding also needs the media controllers (`/dev/media*`) and a read-only
`/run/udev`; without them FFmpeg decodes in software and Settings → Transcoding
says why.

## Running as a service

`deploy/` has a systemd unit for the binary, a Podman Quadlet unit that follows
`:latest` with `podman auto-update`, `lex-hwcodec.service` for Raspberry Pi
codec modules, and `deploy.sh` to install either over SSH. Review paths, users
and device group IDs for your host first.

For remote access, put lex behind an HTTPS reverse proxy or a VPN. If you
enable **Trust reverse-proxy headers**, list every proxy in **Trusted proxy
peers**. Clients outside **Local networks** get the **Remote bitrate limit**.
Settings → Server → **Sonarr & Radarr** connects lex's rescan webhook with one
click (lex adds it through their API, trying its addresses until the app's own
Test passes). The webhook URL carries its own token; keep it private.

See [backups](backups.md) for backing up and restoring the database.
