#!/usr/bin/env bash
# Builds release archives for every supported platform into dist/.
# Usage: scripts/build-release.sh [tag]   (default: v + internal/version/VERSION)
set -euo pipefail
cd "$(dirname "$0")/.."
TAG=${1:-v$(cat internal/version/VERSION)}
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || true)
TARGETS="linux/amd64 linux/arm64 linux/arm/7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
rm -rf dist
mkdir -p dist
for target in $TARGETS; do
  IFS=/ read -r os arch arm <<< "$target"
  name="lex-$TAG-$os-$arch${arm:+v$arm}"
  dir="dist/$name"
  bin=lex
  [ "$os" = windows ] && bin=lex.exe
  mkdir -p "$dir"
  echo "building $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch GOARM=$arm \
    go build -trimpath -ldflags "-s -w -X lex/internal/version.Commit=$COMMIT" -o "$dir/$bin" ./cmd/lex
  cp LICENSE README.md THIRD_PARTY_NOTICES.md "$dir/"
  if [ "$os" = windows ]; then
    (cd dist && zip -qr "$name.zip" "$name")
  else
    tar -C dist -czf "dist/$name.tar.gz" "$name"
  fi
  rm -rf "$dir"
done
cd dist
if command -v sha256sum >/dev/null; then sha256sum lex-* > SHA256SUMS; else shasum -a 256 lex-* > SHA256SUMS; fi
ls -l
