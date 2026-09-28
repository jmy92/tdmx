<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo/tdm-wordmark.svg">
    <img src="./assets/logo/tdm-wordmark-light-bg.svg" alt="TDM" width="380">
  </picture>
</p>

<div align="center">

[简体中文](./README.md) · **English**

</div>

# TDM — GUI Download Manager

A cross-platform download manager built with Go + Wails v3, offering both a graphical interface (GUI) and a terminal interface (TUI).

- **HTTP/HTTPS** multi-connection chunked downloads with resume support
- **BitTorrent** magnet links and torrent files
- A modern desktop UI with light/dark themes

> If you find this project useful, please give it a star! Suggestions and feedback are always welcome — leave a comment and I'll get back to you as soon as possible.

## Features

### Download Engine

- **HTTP/HTTPS**
  - Multi-threaded downloads: up to 32 chunks and 8 concurrent connections per task
  - Automatic fallback to a single connection when the server doesn't support Range requests
  - Resume support: continues from where it left off after a restart; re-merges automatically if all chunks are complete but the final file is missing
  - Chunk temp directory lives under the download directory (`<download dir>/.tdm-temp`), so merging never touches the system drive
  - Name collision protection: existing files are preserved as `filename (n).ext`

- **BitTorrent** (paste a torrent file URL or a magnet link)
  - Direct torrent file links and magnet links
  - DHT, PEX, and tracker support
  - Optional seeding after completion

### Task Management

- Priority queue: higher number starts first (P1–P10, adjustable on each task card)
- Pause / resume / cancel / delete
- Tasks sorted by creation time, newest first
- One-click "open folder" for finished tasks; tasks whose files were moved or deleted are marked as "missing"
- Automatic download link detection from the clipboard
- Paste multiple links at once to batch-create tasks

### Graphical Interface

- Light / dark theme switching, remembered across sessions
- Live speed, progress, and ETA display
- Global aggregate speed
- Save directory can be changed in-app and is persisted
- Frameless custom window (window controls rendered in-page)

## Building from Source

Requires Go 1.24+ (Go 1.27+ recommended for Windows packaging).

### GUI Build (Windows)

```powershell
git clone https://github.com/jmy92/tdmx.git
cd tdmx

# Frontend assets (frontend/dist is the go:embed directory; re-sync after editing the frontend)
Copy-Item cmd\tdm-gui\frontend\* cmd\tdm-gui\frontend\dist\

# Embed icon / version info / manifest (rsrc_windows_amd64.syso is committed to the repo;
# regenerate after changing winres/winres.json or the icon)
go-winres make --in winres\winres.json --out cmd\tdm-gui\rsrc --arch amd64 --no-suffix
Move-Item cmd\tdm-gui\rsrc cmd\tdm-gui\rsrc_windows_amd64.syso -Force

# Build (GUI binary without a console window)
go build -trimpath -ldflags "-s -w -H windowsgui" -o tdm-gui.exe ./cmd/tdm-gui
```

> Windows resource note: Wails v3 loads the **ID=3** icon resource as the window/taskbar icon.
> The icon group in `winres/winres.json` is named `#3`, and the Explorer file icon comes from
> the same resource.

### TUI Build

```bash
go build -o tdm
./tdm
```

## Configuration

The config file lives at `~/.config/tdm` (Linux/macOS) or `%LOCALAPPDATA%/tdm` (Windows) and is
created on first run. Settings changed in the GUI (e.g. the save directory) are written back
automatically.

```yaml
maxConcurrentDownloads: 3            # Max concurrent downloads

http:
  dir: "download dir"                # Save directory for new tasks (default: system Downloads)
  tempDir: "download dir/.tdm-temp"  # Chunk temp dir (defaults to the download dir to avoid cross-drive merges)
  connections: 8                     # Concurrent connections per task
  maxChunks: 32                      # Max chunks per HTTP download
  maxRetries: 3                      # Retries per chunk failure
  retryDelay: 2s                     # Delay between retries

torrent:
  dir: "download dir"                # Torrent download directory
  seed: true                         # Keep seeding after download completes
  establishedConnectionsPerTorrent: 50
  halfOpenConnectionsPerTorrent: 25
  totalHalfOpenConnections: 100
  disableDht: false                  # Disable DHT
  disablePex: false                  # Disable PEX
  disableTrackers: false             # Disable trackers
  disableIPv6: false
  metainfoTimeout: 60s               # Metadata fetch timeout
```

The TUI also accepts command-line flags (which take precedence over the config file); see `tdm -h`.

## Project Layout

```
cmd/tdm-gui/        GUI entry point + embedded frontend assets
gui/                Wails bindings (bridge between engine and frontend)
internal/
  config/           Config loading and defaults
  download/         Core download data model and progress tracking
  downloaders/http/ HTTP multi-threaded engine (chunking / merging / resume)
  downloaders/torrent/ BitTorrent engine
  manager/          Task scheduling and lifecycle management
  store/boltdb/     Task persistence (BoltDB)
  tui/              Terminal interface
pkg/
  http/             HTTP client wrapper
  torrent/          Torrent client wrapper
winres/             Windows resources (icon / version / manifest)
assets/icon/        App icon sources and PNG/ICO in all sizes
```

## Acknowledgements

This project is based on [NamanBalaji/tdm](https://github.com/NamanBalaji/tdm), with a graphical
interface added on top of its terminal download engine.

## License

[MIT](./LICENSE)
