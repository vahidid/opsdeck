// Command opsdeck runs the local server and web UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"opsdeck/internal/paths"
	"opsdeck/internal/server"
)

var version = "0.1.0-dev"

func usage() {
	fmt.Fprintf(os.Stderr, `opsdeck %s — local workbench and AI agent harness for infrastructure

Usage:
  opsdeck [serve] [flags]   start the server and open the UI (default)
  opsdeck url               print the login URL
  opsdeck paths             show where data is stored
  opsdeck version

Flags for serve:
`, version)
	flag.PrintDefaults()
}

func main() {
	port := flag.Int("port", 7788, "listen port (always bound to 127.0.0.1)")
	noOpen := flag.Bool("no-open", false, "do not open the browser")
	devOrigin := flag.String("dev-origin", "", "extra allowed Origin for UI development, e.g. http://localhost:5173")
	flag.Usage = usage
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	flag.CommandLine.Parse(args)
	dirs := paths.Default()

	switch cmd {
	case "version":
		fmt.Println(version)
		return
	case "paths":
		fmt.Printf("data:   %s\nstate:  %s\nconfig: %s\n", dirs.Data, dirs.State, dirs.Config)
		return
	case "url":
		srv, err := server.New(dirs, server.Options{Port: *port})
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(srv.URL())
		srv.Close()
		return
	case "serve":
	default:
		usage()
		os.Exit(2)
	}

	srv, err := server.New(dirs, server.Options{Port: *port, DevOrigin: *devOrigin})
	if err != nil {
		log.Fatalf("opsdeck: %v", err)
	}
	addr := "127.0.0.1:" + strconv.Itoa(*port)
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()

	fmt.Printf("\n  opsdeck %s\n\n  Open: %s\n\n  Data: %s\n  Press Ctrl+C to stop.\n\n", version, srv.URL(), dirs.Data)
	if !*noOpen {
		go func() {
			time.Sleep(400 * time.Millisecond)
			openBrowser(srv.URL())
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errc:
		srv.Close()
		log.Fatalf("opsdeck: %v", err)
	case <-sig:
		fmt.Println("\nshutting down…")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(ctx)
	srv.Close()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
