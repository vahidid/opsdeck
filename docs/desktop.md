# Desktop app

`cmd/opsdeck-desktop` is opsdeck as an installable app for Linux, Windows and macOS. It
is the same program as `opsdeck serve`, with the UI in its own window instead of a browser
tab:

- The app starts the usual local server on `127.0.0.1`, on port 7788 when it is free, so
  the UI keeps the theme and tabs you use in the browser. If the port is taken, it uses a
  free port instead. When `opsdeck serve` is already running with the same data, the
  window shows that server instead of starting a second one.
- The window is a [Wails](https://wails.io) (v2) window that shows the system webview:
  WebView2 on Windows, WKWebView on macOS, WebKitGTK on Linux. It loads the server's
  login link, so everything in the UI works over the same HTTP API, WebSockets and SSE
  as in a browser. While the window is open you can still reach the UI in a browser
  (`opsdeck url` prints the link).
- Data is shared with the CLI: the vault, workspaces and settings live in the same folders
  (see the README's *Data* section), and `OPSDECK_HOME` works the same way.
- One instance per data folder. Starting the app again brings up the open window.
  Closing the window quits opsdeck, which disconnects VPNs and port forwards and ends
  agent sessions.
- The log goes to `desktop.log` in the state folder (`opsdeck paths`). The previous
  run's log is kept as `desktop.log.old`.

## What the desktop app adds

A webview can't open new browser windows or save downloads, so the UI loads with
`?desktop=1` and then hands these to the server (`internal/server/desktop.go`,
`web/src/lib/desktop.ts`). Those endpoints only exist in desktop mode.

- **Web links** (Git hosting pages, merge requests, docs, VPN login links, panels behind
  port forwards) open in the default browser (`POST /api/desktop/open`, http and https
  only).
- **Downloads** (log exports, topology exports, files from Docker volumes) are saved in
  the Downloads folder under a free name and shown in the file manager
  (`POST /api/desktop/save`).

Since the app has no terminal, opsdeck starts every helper program (git, ssh, ansible, VPN
clients, agent CLIs) through `internal/sysproc`. On Windows this keeps each one from
opening a console window. On macOS, an app started from the Finder only gets a minimal
`PATH`, so the app reads `PATH` from your login shell at startup. That way git, kubectl
plugins, ansible and agent CLIs from Homebrew or `~/.local/bin` are found.

opsdeck also runs its own executable as an SSH `ProxyCommand` (`socks-connect`) and as a
stdio MCP relay for external agents (`mcp-stdio`). The desktop binary answers both.

## Install

Builds come from the **Desktop** workflow (`.github/workflows/desktop.yml`). Each run
keeps them as workflow artifacts. A `v*` tag also publishes them as a GitHub release.

| System | File | Notes |
|---|---|---|
| Debian, Ubuntu | `opsdeck_<version>_amd64.deb` | `sudo apt install ./opsdeck_*.deb`; needs `libwebkit2gtk-4.1-0` (Ubuntu 22.04+, Debian 12+) |
| Fedora, RHEL | `opsdeck-<version>-1.x86_64.rpm` | `sudo dnf install ./opsdeck-*.rpm` |
| Windows 10/11 | `opsdeck-<version>-windows-x64-setup.exe` | Installs into Program Files with Start menu and desktop shortcuts, and installs the WebView2 runtime if it is missing. Also available as `…-portable.exe`. |
| macOS 12+ | `opsdeck-<version>-macos-universal.dmg` | Apple silicon and Intel. Drag to Applications. |

The builds are **not code-signed yet**:

- **macOS:** the first time, right-click the app → *Open* (or allow it under *System
  Settings → Privacy & Security*).
- **Windows:** SmartScreen asks once. Choose *More info → Run anyway*.

The Linux package installs the app as `/usr/bin/opsdeck-desktop` with a menu entry
named *opsdeck*.

## Build it yourself

You need everything `make build` needs, plus the Wails CLI in the version `go.mod` uses:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v2)
sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev   # Linux only
make desktop                                         # → cmd/opsdeck-desktop/build/bin/
```

`make desktop` builds the web UI, then runs `wails build` in `cmd/opsdeck-desktop`. That
folder is an ordinary Wails project:

- `wails.json` holds the name and version info.
- `build/appicon.png` is the icon that the Windows and macOS icons are made from.
- `build/darwin/Info.plist` lets the window load `http://127.0.0.1` (`NSAllowsLocalNetworking`).
- `build/linux/` holds the nfpm package config, the menu entry and the icon.

To cross-build the Windows installer on Linux, install `nsis` and run
`wails build -platform windows/amd64 -nsis -skipbindings`. The Windows build needs no cgo.

The main package carries the `desktop` build tag, which `wails build` sets. This keeps
`go build ./...`, `go vet ./...` and `go test ./...` working on machines without WebKitGTK.

## Not yet

- Code signing and notarization (Apple Developer ID, a Windows certificate) and
  auto-update.
- A tray icon so VPNs and forwards stay up while the window is closed.
- Linux arm64 and Windows arm64 builds.
