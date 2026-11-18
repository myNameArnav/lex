This release is published as a `linux/arm64` container image (64-bit Raspberry Pi OS and other 64-bit ARM Linux):

```sh
podman pull ghcr.io/mynamearnav/lex:latest
```

There are no prebuilt binaries; build one with `go build -trimpath -o lex ./cmd/lex`. See the [install guide](https://github.com/myNameArnav/lex/blob/main/docs/install.md) for setup.

Lex was built by large language models; see the AI disclosure in the README.
