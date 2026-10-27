//go:build desktop

package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Quitting stops what opsdeck runs before the app exits: agent sessions,
// port forwards, SSH connections and VPNs, whose clients log out of their
// gateways and remove their routes. The window shows each step meanwhile.
// The window's close button, Cmd+Q, the tray's Quit, an update and SIGTERM
// (logging out) all quit this way; with a tray, the close button only hides
// the window there (and on macOS it hides the app, which stays in the Dock).

// shutdownTimeout bounds quitting: the app exits even if a client hangs.
const shutdownTimeout = 30 * time.Second

//go:embed build/tray.png
var logoPNG []byte

// quit quits the app gracefully.
func (a *app) quit() {
	a.mu.Lock()
	a.quitting = true
	a.mu.Unlock()
	if a.ctx != nil {
		wruntime.Quit(a.ctx)
	}
}

// beforeClose is Wails' OnBeforeClose, for every way of closing the window
// or quitting: true keeps the app running.
func (a *app) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	if !a.quitting && a.tray && runtime.GOOS != "darwin" {
		// the window's close button: opsdeck keeps running in the tray
		go wruntime.WindowHide(ctx)
		return true
	}
	if !a.shuttingDown {
		a.shuttingDown = true
		go a.shutdownGracefully(ctx)
	}
	return true
}

// shutdownGracefully shows the shutdown screen, stops everything and quits.
func (a *app) shutdownGracefully(ctx context.Context) {
	wruntime.WindowShow(ctx)
	wruntime.WindowUnminimise(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.stopServer(a.showShutdown)
	}()
	select {
	case <-done:
		a.showShutdown("")
	case <-time.After(shutdownTimeout):
		log.Printf("opsdeck: shutting down took more than %s; quitting anyway", shutdownTimeout)
	}
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	wruntime.Quit(ctx)
}

// stopServer stops the server and what it runs, once.
func (a *app) stopServer(progress func(step string)) {
	a.stopOnce.Do(func() {
		if a.unannounce != nil {
			a.unannounce() // `opsdeck mcp` no longer finds this server
		}
		if a.srv != nil {
			a.srv.Shutdown(progress)
		}
		if a.http != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if a.http.Shutdown(ctx) != nil {
				a.http.Close()
			}
		}
		log.Printf("opsdeck: shut down")
	})
}

// quitOnSignal quits gracefully on SIGTERM and SIGINT (the session ends).
func (a *app) quitOnSignal() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("opsdeck: signal received, quitting")
		a.quit()
	}()
}

// showShutdown shows the shutdown screen in the window with the steps so far
// and the current one ("" = all done). It replaces the page, which no longer
// has a server to talk to.
func (a *app) showShutdown(step string) {
	if a.ctx == nil {
		return
	}
	a.mu.Lock()
	if step != "" && (len(a.steps) == 0 || a.steps[len(a.steps)-1] != step) {
		a.steps = append(a.steps, step)
	}
	steps := append([]string(nil), a.steps...)
	a.mu.Unlock()
	var list strings.Builder
	for i, s := range steps {
		state := "done"
		if i == len(steps)-1 && step != "" {
			state = "now"
		}
		list.WriteString(`<li class="` + state + `"><span class="dot"></span>` + html.EscapeString(s) + `</li>`)
	}
	page, _ := json.Marshal(strings.Replace(shutdownPage, "{{steps}}", list.String(), 1))
	wruntime.WindowExecJS(a.ctx, `(function(){var p=`+string(page)+`;`+
		`if(!document.getElementById("opsdeck-quit")){document.documentElement.innerHTML=p}`+
		`else{document.getElementById("opsdeck-steps").innerHTML=new DOMParser().parseFromString(p,"text/html").getElementById("opsdeck-steps").innerHTML}})()`)
}

var shutdownPage = `<head><meta charset="utf-8"><title>opsdeck</title><style>
html,body{height:100%;margin:0;background:#0a0c0f;color:#c9d1d9;font:14px system-ui,-apple-system,"Segoe UI",sans-serif}
#opsdeck-quit{height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:14px;text-align:center;padding:24px;box-sizing:border-box}
img{width:56px;height:56px;border-radius:14px}
h1{margin:4px 0 0;font-size:18px;font-weight:600;color:#e6edf3}
p{margin:0;color:#8b949e;max-width:420px;line-height:1.5}
ul{list-style:none;margin:10px 0 0;padding:0;text-align:left;min-width:280px}
li{display:flex;align-items:center;gap:10px;padding:5px 0;color:#8b949e}
li.now{color:#e6edf3}
.dot{width:12px;height:12px;flex:none;border-radius:50%;box-sizing:border-box}
li.done .dot{background:#10b981}
li.now .dot{border:2px solid #10b981;border-right-color:transparent;animation:spin .8s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
</style></head><body><div id="opsdeck-quit">
<img alt="" src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(logoPNG) + `">
<h1>Shutting down opsdeck</h1>
<p>Disconnecting VPNs and closing connections, so nothing is left running.</p>
<ul id="opsdeck-steps">{{steps}}</ul>
</div></body>`
