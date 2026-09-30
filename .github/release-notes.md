Download the archive for your system below, unpack it and run `lex` (`lex.exe` on Windows). Lex needs `ffmpeg` and `ffprobe` on the `PATH` (or pass `-ffmpeg` / `-ffprobe`); on Debian, Ubuntu and Raspberry Pi OS: `sudo apt install ffmpeg`.

```sh
./lex -addr :8420 -data ./lex-data
```

Then open http://localhost:8420 and create the admin account. See the README for Docker, systemd and reverse-proxy setups. Check a download against `SHA256SUMS` with `sha256sum -c SHA256SUMS --ignore-missing`.

| Archive | For |
|---|---|
| `linux-amd64` | 64-bit Intel/AMD Linux |
| `linux-arm64` | Raspberry Pi 4/5 and other 64-bit ARM Linux |
| `linux-armv7` | 32-bit Raspberry Pi OS |
| `darwin-arm64` / `darwin-amd64` | macOS on Apple silicon / Intel |
| `windows-amd64` / `windows-arm64` | Windows 10/11 |

Linux is the main platform. On macOS and Windows everything plays, but the dashboard's host statistics (CPU, memory, temperature, network) are Linux-only, and on Windows ffmpeg jobs aren't lowered in priority. The macOS and Windows binaries aren't signed: on macOS run `xattr -d com.apple.quarantine lex` once; on Windows choose "More info → Run anyway".

Lex was built by large language models; see the AI disclosure in the README.
