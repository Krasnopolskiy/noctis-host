# noctis-host

Native messaging helper for the [Noctis](https://noctis.c0nn3ct.xyz) browser extension. Supervises a local [sing-box](https://github.com/SagerNet/sing-box) process and ships logs and events back to the extension over Chrome's native-messaging channel.

This repository is a public mirror — source is synced from the Noctis development repo. Issues and pull requests welcome here.

## What it does

- Reads native-messaging frames from `stdin`, writes them to `stdout`.
- Generates a sing-box config file from the extension's server list + routing profile.
- Spawns and supervises sing-box, restarts it on crash, streams its `stdout`/`stderr` back to the extension.
- Periodic health checks on the active proxy.

The helper is the only component that touches your filesystem and OS. The extension itself runs only inside the browser.

## Build

```bash
go build -o noctis-host .
```

You need Go 1.21 or newer. To download a matching sing-box for embedding, see `embed/` and the `fetch-singbox.sh` script in the Noctis repo (or download a sing-box release manually and place it on `PATH`).

## Install

Per-OS installer scripts live in `install/`. They:

1. Copy `noctis-host` and `sing-box` into a per-user data directory.
2. Write a `com.noctis.host.json` native-messaging manifest into every supported browser's profile folder.

```bash
# macOS
install/install-macos.sh <extension-id>
```

Linux and Windows installers are placeholders — contributions welcome.

## Protocol

The helper speaks the [Chrome native-messaging protocol](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging): little-endian uint32 length prefix followed by a UTF-8 JSON payload. Message schema is defined by the extension; see `ipc.go` for the dispatch surface.

## License

MIT — see [`LICENSE`](./LICENSE). sing-box is redistributed under its upstream license; see the [sing-box repository](https://github.com/SagerNet/sing-box).
