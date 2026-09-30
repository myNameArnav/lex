# Third-party notices

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
