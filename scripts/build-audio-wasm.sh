#!/bin/sh
# Builds web/static/vendor/lexaudio/: the browser's AC-3/E-AC-3 → AAC
# converter (wasm/audio/audio.c) linked against a minimal, LGPL-only FFmpeg.
# Needs Docker; the result is committed, so this only runs when it changes.
set -eu

FFMPEG_VERSION=7.1.2
EMSDK_IMAGE=emscripten/emsdk:4.0.15

root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/web/static/vendor/lexaudio"
mkdir -p "$out"

docker run --rm --platform linux/amd64 -v "$root:/src" -w /tmp "$EMSDK_IMAGE" sh -euc "
curl -fsSL https://ffmpeg.org/releases/ffmpeg-$FFMPEG_VERSION.tar.xz | tar xJ
cd ffmpeg-$FFMPEG_VERSION
emconfigure ./configure --cc=emcc --cxx=em++ --ar=emar --ranlib=emranlib --nm=emnm \
  --target-os=none --arch=x86_32 --enable-cross-compile --prefix=/tmp/ff \
  --disable-everything --disable-programs --disable-doc --disable-debug \
  --disable-avdevice --disable-avformat --disable-avfilter --disable-swscale --disable-swresample \
  --disable-network --disable-pthreads --disable-w32threads --disable-os2threads \
  --disable-asm --disable-inline-asm --disable-x86asm --disable-runtime-cpudetect \
  --enable-decoder=ac3,eac3 --enable-encoder=aac \
  --extra-cflags=-O3 >/dev/null
emmake make -j\$(nproc) >/dev/null
emmake make install >/dev/null
emcc -O3 -I/tmp/ff/include /src/wasm/audio/audio.c /tmp/ff/lib/libavcodec.a /tmp/ff/lib/libavutil.a \
  -s MODULARIZE=1 -s EXPORT_ES6=1 -s ENVIRONMENT=worker,node -s FILESYSTEM=0 \
  -s ALLOW_MEMORY_GROWTH=1 -s EXPORTED_RUNTIME_METHODS=HEAPU8 \
  -o /src/web/static/vendor/lexaudio/audio.js
cp COPYING.LGPLv2.1 /src/web/static/vendor/lexaudio/LICENSE
"
ls -l "$out"
