#!/usr/bin/env bash
# Deploy to an explicitly selected Linux SSH host with root privileges.
# Review deploy/lex.container and deploy/lex.service for host-specific paths
# and device group IDs first. This replaces the existing Lex deployment.
# Usage: deploy/deploy.sh HOST [arm64|amd64] [container|container-local|binary]
#   container        published image from ghcr.io, updated by podman auto-update
#   container-local  build the image here and copy it over (unreleased changes)
#   binary           systemd service running a binary built here
set -euo pipefail
if [ "$#" -lt 1 ] || [ "$#" -gt 3 ] || [[ "$1" = -* ]]; then
  echo "usage: $0 HOST [arm64|amd64] [container|container-local|binary]" >&2
  exit 2
fi
HOST=$1
ARCH=${2:-arm64}
MODE=${3:-container}
case "$ARCH" in arm64|amd64) ;; *) echo "unsupported architecture: $ARCH" >&2; exit 2 ;; esac
case "$MODE" in container|container-local|binary) ;; *) echo "unsupported mode: $MODE" >&2; exit 2 ;; esac
cd "$(dirname "$0")/.."
VERSION=$(cat internal/version/VERSION)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || true)
command -v ssh >/dev/null
UNIT=$(cat deploy/lex.container)
if [ "$MODE" = container-local ]; then
  command -v docker >/dev/null
  command -v gzip >/dev/null
  echo "building lex:$VERSION image for linux/$ARCH"
  docker build --platform "linux/$ARCH" --build-arg COMMIT="$COMMIT" -t lex:latest .
  # Run the image loaded below instead of the published one.
  UNIT=$(printf '%s\n' "$UNIT" | sed -e 's#^Image=.*#Image=localhost/lex:latest#' -e '/^AutoUpdate=/d')
elif [ "$MODE" = binary ]; then
  command -v go >/dev/null
  mkdir -p build
  echo "building lex $VERSION binary for linux/$ARCH"
  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath -ldflags "-s -w -X lex/internal/version.Commit=$COMMIT" -o "build/lex-linux-$ARCH" ./cmd/lex
fi
# Validate the remote prerequisites before changing service files.
ssh "$HOST" 'test "$(id -u)" = 0 && command -v systemctl >/dev/null'
if [ "$MODE" != binary ]; then
  ssh "$HOST" 'command -v podman >/dev/null'
fi
ssh "$HOST" 'cat > /etc/systemd/system/lex-hwcodec.service' < deploy/lex-hwcodec.service
ssh "$HOST" 'systemctl daemon-reload && systemctl enable --now lex-hwcodec.service >/dev/null 2>&1 || true'
if [ "$MODE" != binary ]; then
  if [ "$MODE" = container-local ]; then
    echo "copying image to $HOST"
    docker save lex:latest | gzip -1 | ssh "$HOST" 'gunzip | podman load >/dev/null && podman tag docker.io/library/lex:latest localhost/lex:latest'
  else
    image=$(printf '%s\n' "$UNIT" | sed -n 's/^Image=//p')
    echo "pulling $image on $HOST"
    ssh "$HOST" "podman pull --quiet '$image' >/dev/null"
  fi
  printf '%s\n' "$UNIT" | ssh "$HOST" 'install -d /etc/containers/systemd; cat > /etc/containers/systemd/lex.container'
  ssh "$HOST" 'set -e
    if [ -f /etc/systemd/system/lex.service ]; then
      systemctl disable --now lex.service || true
      mv /etc/systemd/system/lex.service /etc/systemd/system/lex.service.binary-backup
    fi
    install -d -o 1000 -g 1000 -m 700 /var/lib/lex
    # Files left by the binary deployment belong to the "lex" system user;
    # the container runs as UID 1000. -h: never follow symlinks.
    chown -hR 1000:1000 /var/lib/lex
    systemctl daemon-reload
    systemctl restart lex.service
    systemctl --no-pager --lines=5 status lex.service'
  exit 0
fi
REMOTE_TMP=$(ssh "$HOST" 'mktemp -d /tmp/lex-deploy.XXXXXXXXXX')
if [[ ! "$REMOTE_TMP" =~ ^/tmp/lex-deploy\.[A-Za-z0-9]+$ ]]; then
  echo "invalid remote staging directory" >&2
  exit 1
fi
trap 'ssh "$HOST" "rm -rf -- $REMOTE_TMP" >/dev/null 2>&1 || true' EXIT
ssh "$HOST" "cat > $REMOTE_TMP/lex.new" < "build/lex-linux-$ARCH"
ssh "$HOST" "cat > $REMOTE_TMP/lex.service" < deploy/lex.service
ssh "$HOST" sh -s -- "$REMOTE_TMP" <<'REMOTE_SCRIPT'
  set -e
  staging=$1
  command -v ffmpeg >/dev/null || (apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ffmpeg)
  id lex >/dev/null 2>&1 || useradd --system --user-group --no-create-home --home-dir /var/lib/lex --shell /usr/sbin/nologin lex
  for group in video render; do getent group "$group" >/dev/null && usermod -aG "$group" lex; done
  systemctl stop lex.service || true
  rm -f /etc/containers/systemd/lex.container
  install -d -m 755 /opt/lex
  install -d -o lex -g lex -m 700 /var/lib/lex
  # Files left by a container deployment belong to UID 1000.
  chown -hR lex:lex /var/lib/lex
  install -m 755 "$staging/lex.new" /opt/lex/lex
  install -m 644 "$staging/lex.service" /etc/systemd/system/lex.service
  systemctl daemon-reload
  systemctl enable lex >/dev/null 2>&1
  systemctl restart lex
  systemctl --no-pager --lines=5 status lex
REMOTE_SCRIPT
