# Third-party notices

## FFmpeg 7.1.2 (`web/static/vendor/lexaudio/`)

The browser's AC-3/E-AC-3 → AAC audio converter. `audio.wasm` and its
loader `audio.js` are built by `scripts/build-audio-wasm.sh` from
[FFmpeg 7.1.2](https://ffmpeg.org/releases/ffmpeg-7.1.2.tar.xz) (only
libavcodec's AC-3/E-AC-3 decoders and AAC encoder, plus libavutil, without
GPL or nonfree components) linked with Lex's `wasm/audio/audio.c`.
FFmpeg is licensed under the GNU Lesser General Public License 2.1 or later;
see `web/static/vendor/lexaudio/LICENSE`. The FFmpeg source is available at the
link above, and the script rebuilds the files, which can be replaced with
your own build.

## JASSUB 2.5.16 (`web/static/vendor/jassub/`)

Browser subtitle renderer, bundled unmodified from the npm package
[`jassub@2.5.16`](https://github.com/ThaUnknown/jassub) (JavaScript rebundled
with esbuild; WebAssembly files as published). MIT License; see
`web/static/vendor/jassub/LICENSE`.

The WebAssembly files are compiled from JASSUB's sources and include these
libraries under their own licenses:

| Component | License |
|-----------|---------|
| [libass](https://github.com/libass/libass) | ISC |
| [FreeType](https://freetype.org/) | FreeType License (FTL) |
| [HarfBuzz](https://github.com/harfbuzz/harfbuzz) | MIT ("Old MIT") |
| [FriBidi](https://github.com/fribidi/fribidi) | LGPL-2.1-or-later |

The FriBidi source and the build scripts that produce the WebAssembly files
are available from the JASSUB repository at the matching tag. The WebAssembly
files can be replaced with your own build of the same version.

`default.woff2` is **Liberation Sans**, licensed under the
[SIL Open Font License 1.1](https://github.com/liberationfonts/liberation-fonts/blob/main/LICENSE).
