//go:build windows

package vpn

import (
	"errors"
	"opsdeck/internal/sysproc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// On Windows (no inheritable fds 3/4) the browser listens on a random
// local port for the time of the login.
func startBrowser(exe string, args []string, profileDir string) (*exec.Cmd, cdpTransport, error) {
	os.Remove(filepath.Join(profileDir, "DevToolsActivePort"))
	cmd := sysproc.Command(exe, append(args, "--remote-debugging-port=0", "about:blank")...)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	var port, path string
	for i := 0; i < 100; i++ {
		b, err := os.ReadFile(filepath.Join(profileDir, "DevToolsActivePort"))
		if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); err == nil && len(lines) == 2 {
			port, path = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if port == "" {
		cmd.Process.Kill()
		return nil, nil, errors.New("the browser did not open its DevTools port")
	}
	c, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+port+path, nil)
	if err != nil {
		cmd.Process.Kill()
		return nil, nil, err
	}
	return cmd, &wsTransport{c: c}, nil
}

type wsTransport struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (w *wsTransport) send(b []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.c.WriteMessage(websocket.TextMessage, b)
}

func (w *wsTransport) recv() ([]byte, error) {
	_, b, err := w.c.ReadMessage()
	return b, err
}

func (w *wsTransport) close() { w.c.Close() }
