# syntax=docker/dockerfile:1
# Lex media server.
#   docker build -t lex .                      (native arch)
#   docker buildx build --platform linux/arm64 -t lex .
#
# FFmpeg is compiled here instead of installed from apt. The distribution
# packages enable every optional library (LLVM/Mesa via libplacebo, speech
# synthesis, SDL, x265, ...), which made the image over 500 MB; Lex needs only
# FFmpeg's built-in components plus x264, dav1d, zimg, chromaprint and libdrm.
# On arm64 the source carries the Raspberry Pi archive's patch, which adds the
# Pi's hardware HEVC decoder (-hwaccel drm). Build with --build-arg
# RPI_FFMPEG=no for plain upstream FFmpeg on other arm64 boards.

FROM --platform=$BUILDPLATFORM golang:1.26.11-trixie AS build
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
    go build -trimpath -tags timetzdata -ldflags "-s -w -X lex/internal/version.Commit=$COMMIT" -o /out/lex ./cmd/lex

# FFmpeg is compiled on the build machine's architecture, cross-compiling for
# the target, so CI doesn't compile it under QEMU. (The native gcc builds
# FFmpeg's build-time helper tools.)
FROM --platform=$BUILDPLATFORM debian:trixie AS ffmpeg
ARG TARGETARCH
ARG RPI_FFMPEG=yes
# Source of the Raspberry Pi archive's ffmpeg package (upstream release plus
# one patch). To update: take the newest ffmpeg_*+rpt*.dsc from
# https://archive.raspberrypi.com/debian/pool/main/f/ffmpeg/ and copy its
# version and the sha256 sums of its two tarballs here.
ARG FFMPEG_VERSION=7.1.5
ARG FFMPEG_RPI_REVISION=0+deb13u1+rpt2
ARG FFMPEG_SHA256=de668509caf9e35e3cd162473441fdb29538c6d96ed080292b3cf9e6fc5d558f
ARG FFMPEG_RPI_SHA256=2d443066737e8a7b6163bee6cba30519532e7ccffa9358bd4e455c49d9ebf01e
RUN set -eux; \
    build_arch="$(dpkg --print-architecture)"; \
    case "$TARGETARCH" in \
      arm64) triplet=aarch64-linux-gnu; arch=aarch64 ;; \
      amd64) triplet=x86_64-linux-gnu; arch=x86_64 ;; \
      *) echo "unsupported TARGETARCH $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    rpi=no; \
    if [ "$TARGETARCH" = "arm64" ] && [ "$RPI_FFMPEG" = "yes" ]; then rpi=yes; fi; \
    if [ "$build_arch" = "$TARGETARCH" ]; then \
      toolchain="gcc libc6-dev"; cross=""; \
    else \
      dpkg --add-architecture "$TARGETARCH"; \
      toolchain="gcc libc6-dev crossbuild-essential-$TARGETARCH"; \
      cross="--enable-cross-compile --cross-prefix=$triplet- --target-os=linux"; \
    fi; \
    devs="libx264-dev libdav1d-dev libzimg-dev libchromaprint-dev libdrm-dev zlib1g-dev libbz2-dev liblzma-dev"; \
    if [ "$rpi" = "yes" ]; then devs="$devs libudev-dev"; fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl xz-utils make patch pkgconf $toolchain; \
    if [ "$TARGETARCH" = "amd64" ]; then apt-get install -y --no-install-recommends nasm; fi; \
    apt-get install -y --no-install-recommends $(for p in $devs; do echo "$p:$TARGETARCH"; done); \
    mkdir /src; cd /src; \
    curl -fsSLo ffmpeg.tar.xz "https://archive.raspberrypi.com/debian/pool/main/f/ffmpeg/ffmpeg_${FFMPEG_VERSION}.orig.tar.xz"; \
    echo "$FFMPEG_SHA256  ffmpeg.tar.xz" | sha256sum -c -; \
    tar -xJf ffmpeg.tar.xz --strip-components=1; \
    rpi_flags=""; \
    if [ "$rpi" = "yes" ]; then \
      curl -fsSLo debian.tar.xz "https://archive.raspberrypi.com/debian/pool/main/f/ffmpeg/ffmpeg_${FFMPEG_VERSION}-${FFMPEG_RPI_REVISION}.debian.tar.xz"; \
      echo "$FFMPEG_RPI_SHA256  debian.tar.xz" | sha256sum -c -; \
      tar -xJf debian.tar.xz; \
      while read -r p; do patch -p1 < "debian/patches/$p"; done < debian/patches/series; \
      rpi_flags="--enable-v4l2-request --enable-libudev --enable-sand"; \
    fi; \
    # Everything built into FFmpeg (decoders, demuxers, filters, ...) stays;
    # only external libraries are opt-in. lavfi is the one input device Lex
    # uses (test sources); the rest of libavdevice is left out.
    PKG_CONFIG_LIBDIR="/usr/lib/$triplet/pkgconfig:/usr/share/pkgconfig" \
    ./configure $cross --arch=$arch --pkg-config=pkg-config \
      --prefix=/usr/local --toolchain=hardened \
      --enable-gpl --enable-shared --disable-static \
      --disable-doc --disable-ffplay --disable-debug --disable-autodetect \
      --disable-indevs --enable-indev=lavfi --disable-outdevs \
      --enable-pthreads --enable-zlib --enable-bzlib --enable-lzma --enable-iconv \
      --enable-v4l2-m2m --enable-libdrm \
      --enable-libx264 --enable-libdav1d --enable-libzimg --enable-chromaprint \
      $rpi_flags; \
    make -j"$(nproc)"; \
    make install DESTDIR=/out; \
    rm -rf /out/usr/local/include /out/usr/local/share /out/usr/local/lib/pkgconfig

# The runtime filesystem is assembled from Debian's packages but ships only
# what Lex runs: FFmpeg, ffprobe and vcgencmd with the shared libraries they
# load, glibc's iconv module for subtitle charset conversion, CA certificates
# and os-release. No shell, apt or coreutils: the base OS was about 100 MB of
# the 150 MB image. The binaries are the same as before; only the files
# around them are gone.
FROM debian:trixie-slim AS rootfs
ARG TARGETARCH
ARG RPI_FFMPEG=yes
# Raspberry Pi archive keys (from the raspberrypi-archive-keyring package;
# the key published on their website only has SHA-1 bindings, which trixie
# rejects).
COPY docker/raspberrypi-archive-*.pgp /usr/share/keyrings/
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates \
      libx264-164 libdav1d7 libzimg2 libchromaprint1 libdrm2 libudev1 libbz2-1.0 liblzma5 zlib1g; \
    if [ "$TARGETARCH" = "arm64" ] && [ "$RPI_FFMPEG" = "yes" ]; then \
      echo "deb [signed-by=/usr/share/keyrings/raspberrypi-archive-keyring.pgp,/usr/share/keyrings/raspberrypi-archive-automatic.pgp] https://archive.raspberrypi.com/debian trixie main" > /etc/apt/sources.list.d/raspberrypi.list; \
      apt-get update; \
      apt-get install -y --no-install-recommends raspi-utils-core || true; \
    fi
COPY --from=ffmpeg /out/usr/local/ /usr/local/
RUN set -eux; \
    ldconfig; \
    mkdir -p /rootfs/etc /rootfs/tmp /rootfs/data; \
    chmod 1777 /rootfs/tmp; chmod 0700 /rootfs/data; \
    # Debian's merged /usr: /lib -> usr/lib etc. (the ELF interpreter path).
    for l in bin sbin lib lib64; do if [ -L "/$l" ]; then cp -a "/$l" /rootfs/; fi; done; \
    # Copy a path under its canonical directory, plus the file a symlink
    # points to.
    put() { p="$(readlink -f "$(dirname "$1")")/$(basename "$1")"; \
            cp -a --parents "$p" /rootfs/; \
            t="$(readlink -f "$p")"; [ "$t" = "$p" ] || cp -a --parents "$t" /rootfs/; }; \
    bins="/usr/local/bin/ffmpeg /usr/local/bin/ffprobe"; \
    if command -v vcgencmd >/dev/null; then bins="$bins $(command -v vcgencmd)"; fi; \
    for b in $bins; do \
      put "$b"; \
      for f in $(ldd "$b" | awk '$2 == "=>" && $3 ~ /^\// { print $3 } $1 ~ /^\// { print $1 }'); do put "$f"; done; \
    done; \
    # iconv modules: only the charsets Lex passes to -sub_charenc (all 19 MB
    # of them otherwise). Add a module here when Lex learns another one.
    gconv="$(dirname "$(find /usr/lib -name CP1252.so)")"; \
    for f in "$gconv"/gconv-modules* "$gconv"/CP1252.so; do cp -a --parents "$f" /rootfs/; done; \
    put /etc/ssl/certs/ca-certificates.crt; \
    put /etc/os-release; \
    cp -a /etc/ld.so.cache /rootfs/etc/; \
    printf 'root:x:0:0:root:/root:/usr/sbin/nologin\nlex:x:1000:1000:Lex:/data:/usr/sbin/nologin\n' > /rootfs/etc/passwd; \
    printf 'root:x:0:\nvideo:x:44:lex\nlex:x:1000:\n' > /rootfs/etc/group; \
    # Everything above has to work with nothing else around it.
    chroot /rootfs ffmpeg -hide_banner -version >/dev/null; \
    chroot /rootfs ffprobe -hide_banner -version >/dev/null; \
    printf '1\n00:00:00,000 --> 00:00:01,000\ncaf\351\n' > /rootfs/tmp/t.srt; \
    chroot /rootfs ffmpeg -hide_banner -loglevel error -sub_charenc CP1252 -i /tmp/t.srt -f webvtt /tmp/t.vtt; \
    grep -q 'café' /rootfs/tmp/t.vtt; \
    rm /rootfs/tmp/t.*

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/myNameArnav/lex" \
      org.opencontainers.image.title="Lex" \
      org.opencontainers.image.description="Lightweight self-hosted media server for small Linux hosts such as the Raspberry Pi" \
      org.opencontainers.image.licenses="MIT"
COPY --from=rootfs /rootfs/ /
COPY --from=rootfs --chown=1000:1000 /rootfs/data /data
COPY --from=build /out/lex /usr/local/bin/lex
ENV PATH=/usr/local/bin:/usr/bin LEX_ADDR=:8420 LEX_DATA=/data GOMEMLIMIT=96MiB GOGC=50
USER lex
EXPOSE 8420
VOLUME ["/data"]
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s CMD ["lex", "-healthcheck"]
ENTRYPOINT ["lex"]
