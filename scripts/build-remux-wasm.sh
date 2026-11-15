#!/bin/sh
# Builds web/static/vendor/lexremux/: the browser's MKV/WebM → fragmented MP4
# remuxer (wasm/remux/remux.c) linked against a minimal, LGPL-only FFmpeg
# (Matroska demuxer, MP4 muxer, the parsers and bitstream filters a stream
# copy needs). The result is committed, so this only runs when it changes.
#
# Runs emcc natively when it's on PATH (e.g. after sourcing emsdk_env.sh),
# otherwise in the emsdk image with docker or podman (EMSDK_RUNNER="sudo
# podman" for rootful podman). Set LEX_WASM_BUILD to keep the FFmpeg build
# between runs, and JOBS to limit parallel compiles on small machines.
set -eu

FFMPEG_VERSION=7.1.2
EMSDK_VERSION=4.0.15

root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/web/static/vendor/lexremux"
mkdir -p "$out"

build='
cd "$BUILD"
if [ ! -d ffmpeg-$FFMPEG_VERSION ]; then
  curl -fsSL https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz | tar xJ
fi
flags="--cc=emcc --cxx=em++ --ar=emar --ranlib=emranlib --nm=emnm \
  --target-os=none --arch=x86_32 --enable-cross-compile --prefix=$BUILD/ff \
  --disable-everything --disable-autodetect --disable-programs --disable-doc --disable-debug \
  --disable-avdevice --disable-avfilter --disable-swscale --disable-swresample \
  --disable-network --disable-pthreads --disable-w32threads --disable-os2threads \
  --disable-asm --disable-inline-asm --disable-x86asm --disable-runtime-cpudetect \
  --enable-demuxer=matroska --enable-muxer=mp4 \
  --enable-parser=h264,hevc,av1,vp9,aac,ac3,flac,opus,mpegaudio \
  --enable-bsf=aac_adtstoasc,vp9_superframe \
  --extra-cflags=-O3"
if [ "$(cat ff/flags 2>/dev/null)" != "$flags" ]; then
  rm -rf ff
  cd ffmpeg-$FFMPEG_VERSION
  make distclean >/dev/null 2>&1 || true
  emconfigure ./configure $flags >/dev/null
  emmake make -j$JOBS >/dev/null
  emmake make install >/dev/null
  cd ..
  echo "$flags" > ff/flags
fi
emcc -O3 -I"$BUILD/ff/include" "$SRC/wasm/remux/remux.c" \
  "$BUILD/ff/lib/libavformat.a" "$BUILD/ff/lib/libavcodec.a" "$BUILD/ff/lib/libavutil.a" \
  -s MODULARIZE=1 -s EXPORT_ES6=1 -s ENVIRONMENT=worker,node -s FILESYSTEM=0 \
  -s ALLOW_MEMORY_GROWTH=1 -s EXPORTED_RUNTIME_METHODS=HEAPU8,UTF8ToString \
  -o "$SRC/web/static/vendor/lexremux/remux.js"
cp ffmpeg-$FFMPEG_VERSION/COPYING.LGPLv2.1 "$SRC/web/static/vendor/lexremux/LICENSE"
'

jobs=${JOBS:-2}
if command -v emcc >/dev/null 2>&1 && [ -z "${EMSDK_RUNNER:-}" ]; then
  BUILD=${LEX_WASM_BUILD:-$(mktemp -d)}
  mkdir -p "$BUILD"
  BUILD=$BUILD SRC=$root FFMPEG_VERSION=$FFMPEG_VERSION JOBS=$jobs sh -euc "$build"
else
  runner=${EMSDK_RUNNER:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
  image=emscripten/emsdk:$EMSDK_VERSION
  # The emsdk image is published per architecture; pick the native one.
  case $(uname -m) in aarch64 | arm64) image=$image-arm64 ;; esac
  BUILD=${LEX_WASM_BUILD:-}
  cache=""
  if [ -n "$BUILD" ]; then mkdir -p "$BUILD"; cache="-v $BUILD:/build"; fi
  # shellcheck disable=SC2086 # $runner may be "sudo podman"; $cache is optional.
  $runner run --rm -v "$root:/src" $cache -e BUILD=/build -e SRC=/src \
    -e FFMPEG_VERSION=$FFMPEG_VERSION -e JOBS=$jobs "docker.io/$image" sh -euc "mkdir -p /build; $build"
  # Files written by a rootful container belong to root.
  if [ "$(stat -c %u "$out/remux.js")" != "$(id -u)" ]; then sudo chown -R "$(id -u):$(id -g)" "$out"; fi
fi
ls -l "$out"
