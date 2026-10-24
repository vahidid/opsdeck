//go:build desktop

// Command opsdeck-desktop is opsdeck as an installable desktop app: it runs
// the same local server as `opsdeck serve` (on 127.0.0.1, port 7788 when
// free) and shows its UI in a native window (Wails, the system webview).
//
// Build with `wails build` in this folder (see docs/desktop.md); plain
// `go build ./...` skips it, as it needs the `desktop` build tag.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"opsdeck/internal/gitx"
	"opsdeck/internal/mcpx"
	"opsdeck/internal/paths"
	"opsdeck/internal/server"
)

var version = "0.2.0-dev"

// defaultPort is the port of `opsdeck serve`: the UI keeps its settings
// (theme, tabs) per address, so the app and the browser share them.
const defaultPort = 7788

func main() {
	// opsdeck runs its own executable for these (an ssh ProxyCommand, and a
	// stdio MCP relay for external agents), so the app must answer them too.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "socks-connect":
			if err := gitx.SocksConnect(os.Args[2:], os.Stdin, os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "opsdeck socks-connect:", err)
				os.Exit(1)
			}
			return
		case "mcp-stdio":
			fs := flag.NewFlagSet("mcp-stdio", flag.ExitOnError)
			url := fs.String("url", "", "opsdeck MCP endpoint")
			fs.Parse(os.Args[2:])
			if err := mcpx.RelayStdio(*url, version); err != nil {
				fmt.Fprintln(os.Stderr, "opsdeck mcp-stdio:", err)
				os.Exit(1)
			}
			return
		case "version", "--version":
			fmt.Println(version)
			return
		}
	}

	a := &app{dirs: paths.Default(), ready: make(chan struct{})}
	if err := a.dirs.Ensure(); err != nil {
		a.err = err
	} else if a.primary = lockData(filepath.Join(a.dirs.State, "desktop.lock")); a.primary {
		a.logTo(filepath.Join(a.dirs.State, "desktop.log"))
	}
	loginShellEnv()

	err := wails.Run(&options.App{
		Title:     "opsdeck",
		Width:     1440,
		Height:    900,
		MinWidth:  900,
		MinHeight: 600,
		// the UI's dark background, so the window doesn't flash white
		BackgroundColour:         &options.RGBA{R: 10, G: 12, B: 15, A: 255},
		AssetServer:              &assetserver.Options{Handler: http.HandlerFunc(a.loader)},
		Menu:                     appMenu(),
		EnableDefaultContextMenu: true, // copy and paste in fields
		OnStartup:                a.startup,
		OnShutdown:               a.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "io.opsdeck.desktop." + idFor(a.dirs.Data),
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { a.focus() },
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{Title: "opsdeck " + version, Message: "Local workbench and AI agent harness for infrastructure."},
		},
		Linux: &linux.Options{ProgramName: "opsdeck"},
	})
	if err != nil {
		log.Fatal(err)
	}
}

type app struct {
	dirs    paths.Dirs
	primary bool // holds the data folder's lock: the one instance that serves it
	ctx     context.Context
	ready   chan struct{} // closed once url or err is set
	url     string        // UI address with the login token
	err     error

	srv  *server.Server // nil when another opsdeck already serves this data
	http *http.Server
}

// startup runs the server. A second launch hands over to the running
// instance inside wails.Run (which focuses its window) and exits; on Linux
// that happens while startup already runs, so only the instance holding the
// data folder's lock may serve it.
func (a *app) startup(ctx context.Context) {
	a.ctx = ctx
	defer close(a.ready)
	if a.err != nil {
		return
	}
	if !a.primary {
		a.err = errors.New("opsdeck is already open with this data folder (" + a.dirs.Data + ")")
		return
	}
	a.url, a.err = a.serve()
	if a.err != nil {
		log.Printf("opsdeck: %v", a.err)
	}
}

func (a *app) serve() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(defaultPort))
	if err != nil {
		// `opsdeck serve` may already run for the same data: show it.
		if u, ok := a.running(defaultPort); ok {
			log.Printf("using the opsdeck server already running on port %d", defaultPort)
			return u, nil
		}
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "", err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv, err := server.New(a.dirs, server.Options{Port: port, Desktop: true, Version: version,
		// an update quits the app; its installer step starts it again
		Quit: func() { wruntime.Quit(a.ctx) },
	})
	if err != nil {
		ln.Close()
		return "", err
	}
	a.srv = srv
	a.http = &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := a.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("opsdeck: %v", err)
		}
	}()
	log.Printf("opsdeck %s serving on 127.0.0.1:%d, data in %s", version, port, a.dirs.Data)
	// ?desktop=1 makes the UI hand links and downloads to the server
	return strings.Replace(srv.URL(), "/#", "/?desktop=1#", 1), nil
}

// running reports whether an opsdeck server for this data folder (it takes
// our login token) listens on port, and returns its login URL.
func (a *app) running(port int) (string, bool) {
	b, err := os.ReadFile(filepath.Join(a.dirs.State, "server.token"))
	tok := strings.TrimSpace(string(b))
	if err != nil || tok == "" {
		return "", false
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Post(base+"/api/auth/login", "application/json", strings.NewReader(`{"token":`+strconv.Quote(tok)+`}`))
	if err != nil {
		return "", false
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return "", false
	}
	return base + "/#token=" + tok, true
}

func (a *app) shutdown(context.Context) {
	if a.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.http.Shutdown(ctx)
	}
	if a.srv != nil {
		a.srv.Close()
	}
}

func (a *app) focus() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowUnminimise(a.ctx)
	wruntime.Show(a.ctx)
}

// loader is the window's first page: it waits for the server and moves on
// to the UI it serves.
func (a *app) loader(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	startErr := errors.New("the server did not start in time")
	select {
	case <-a.ready:
		startErr = a.err
	case <-time.After(60 * time.Second):
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	body := `<p>Starting opsdeck…</p><script>location.replace(` + strconv.Quote(a.url) + `)</script>`
	if startErr != nil {
		body = `<h3>opsdeck could not start</h3><pre>` + html.EscapeString(startErr.Error()) + `</pre><p>Log: ` +
			html.EscapeString(filepath.Join(a.dirs.State, "desktop.log")) + `</p>`
	}
	fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>opsdeck</title>
<style>body{background:#0a0c0f;color:#c9d1d9;font:14px system-ui,sans-serif;padding:32px}pre{white-space:pre-wrap}</style>
</head><body>`+body+`</body></html>`)
}

// logTo sends the server's log to a file in the state folder (a desktop
// app has no terminal); the previous run's log is kept as .old.
func (a *app) logTo(path string) {
	os.Rename(path, path+".old")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

// appMenu gives macOS its application and Edit menus (without an Edit menu
// ⌘C/⌘V/⌘A do nothing in the window); other systems need none.
func appMenu() *menu.Menu {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return menu.NewMenuFromItems(menu.AppMenu(), menu.EditMenu(), menu.WindowMenu())
}

// idFor tells data folders apart, so OPSDECK_HOME=… gives a separate
// instance with its own window.
func idFor(dir string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(dir); i++ {
		h = (h ^ uint32(dir[i])) * 16777619
	}
	return strconv.FormatUint(uint64(h), 16)
}
