# syntax=docker/dockerfile:1
# Lex media server.
#   docker build -t lex .                      (native arch)
#   docker buildx build --platform linux/arm64 -t lex .
# On arm64 the Raspberry Pi archive's ffmpeg is used: it adds the Pi's
# hardware HEVC decoder (-hwaccel drm). Build with --build-arg RPI_FFMPEG=no
# to use Debian's ffmpeg instead.

FROM --platform=$BUILDPLATFORM golang:1.26.8-trixie AS build
# COMMIT labels the build (the .git directory isn't part of the build context);
# the version number itself comes from internal/version/VERSION.
ARG TARGETOS TARGETARCH COMMIT=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X lex/internal/version.Commit=$COMMIT" -o /out/lex ./cmd/lex

FROM debian:trixie-slim
LABEL org.opencontainers.image.source="https://github.com/myNameArnav/lex" \
      org.opencontainers.image.title="Lex" \
      org.opencontainers.image.description="Lightweight self-hosted media server for small Linux hosts such as the Raspberry Pi" \
      org.opencontainers.image.licenses="MIT"
ARG TARGETARCH
ARG RPI_FFMPEG=yes
# Raspberry Pi archive keys (from the raspberrypi-archive-keyring package;
# the key published on their website only has SHA-1 bindings, which trixie
# rejects).
COPY docker/raspberrypi-archive-*.pgp /usr/share/keyrings/
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates tzdata; \
    if [ "$TARGETARCH" = "arm64" ] && [ "$RPI_FFMPEG" = "yes" ]; then \
      echo "deb [signed-by=/usr/share/keyrings/raspberrypi-archive-keyring.pgp,/usr/share/keyrings/raspberrypi-archive-automatic.pgp] https://archive.raspberrypi.com/debian trixie main" > /etc/apt/sources.list.d/raspberrypi.list; \
      apt-get update; \
      apt-get install -y --no-install-recommends raspi-utils-core || true; \
    fi; \
    apt-get install -y --no-install-recommends ffmpeg; \
    apt-get autoremove -y; \
    rm -rf /var/lib/apt/lists/*; \
    useradd --uid 1000 --user-group --no-create-home --home-dir /data lex; \
    usermod -aG video lex; \
    mkdir -p /data && chmod 0700 /data && chown lex:lex /data
COPY --from=build /out/lex /usr/local/bin/lex
ENV LEX_ADDR=:8420 LEX_DATA=/data GOMEMLIMIT=96MiB GOGC=50
USER lex
EXPOSE 8420
VOLUME ["/data"]
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s CMD ["lex", "-healthcheck"]
ENTRYPOINT ["lex"]
