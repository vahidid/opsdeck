package connect

import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Forward is a local TCP listener tunnelled to a remote address through a
// resource's path (e.g. to open a web panel that is only reachable via VPN).
type Forward struct {
	ID        string    `json:"id"`
	Workspace string    `json:"workspace"`
	Target    string    `json:"target"`
	Remote    string    `json:"remote"`
	LocalPort int       `json:"localPort"`
	URL       string    `json:"url"`
	Created   time.Time `json:"created"`

	ln net.Listener
}

type Forwards struct {
	mu   sync.Mutex
	list map[string]*Forward
}

func NewForwards() *Forwards { return &Forwards{list: map[string]*Forward{}} }

// Start listens on 127.0.0.1:<random> and pipes connections through dial.
func (f *Forwards) Start(ws, target, remote, scheme string, dial DialFunc) (*Forward, error) {
	f.mu.Lock()
	for _, x := range f.list {
		if x.Workspace == ws && x.Target == target && x.Remote == remote {
			f.mu.Unlock()
			return x, nil
		}
	}
	f.mu.Unlock()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	fw := &Forward{ID: uuid.NewString()[:8], Workspace: ws, Target: target, Remote: remote, LocalPort: port, Created: time.Now().UTC(), ln: ln}
	if scheme != "" {
		fw.URL = fmt.Sprintf("%s://127.0.0.1:%d", scheme, port)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				rc, err := dial(ctx, "tcp", remote)
				cancel()
				if err != nil {
					return
				}
				defer rc.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(rc, c); done <- struct{}{} }()
				go func() { io.Copy(c, rc); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	f.mu.Lock()
	f.list[fw.ID] = fw
	f.mu.Unlock()
	return fw, nil
}

func (f *Forwards) List() []*Forward {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []*Forward{}
	for _, x := range f.list {
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (f *Forwards) Stop(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.list[id]
	if ok {
		x.ln.Close()
		delete(f.list, id)
	}
	return ok
}

// StopWorkspace closes all forwards of a workspace.
func (f *Forwards) StopWorkspace(ws string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, x := range f.list {
		if ws == "" || x.Workspace == ws {
			x.ln.Close()
			delete(f.list, id)
		}
	}
}
