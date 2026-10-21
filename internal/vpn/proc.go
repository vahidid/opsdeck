package vpn

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// proc is a VPN client subprocess. Its output goes to the tunnel log and to
// onLine; it is stopped with SIGINT/SIGTERM (relayed by sudo to the client)
// and killed if it does not exit.
type proc struct {
	cmd  *exec.Cmd
	done *doneOnce
	log  *Log
	mu   sync.Mutex
	tail []string // last lines, for error messages
}

// startProc runs argv (prefixed with elevate when non-empty) with stdin.
func startProc(argv, elevate []string, stdin string, log *Log, onLine func(string)) (*proc, error) {
	full := append(append([]string{}, elevate...), argv...)
	cmd := exec.Command(full[0], full[1:]...)
	setProcAttrs(cmd, len(elevate) > 0)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	p := &proc{cmd: cmd, done: newDone(), log: log}
	log.Printf("$ %s", strings.Join(full, " "))
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			log.add(line)
			p.mu.Lock()
			p.tail = append(p.tail, line)
			if len(p.tail) > 8 {
				p.tail = p.tail[1:]
			}
			p.mu.Unlock()
			if onLine != nil {
				onLine(line)
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		pw.Close()
		if err == nil {
			err = fmt.Errorf("%s exited", argv[0])
		} else {
			err = fmt.Errorf("%s: %v", argv[0], err)
		}
		if t := p.lastLines(); t != "" {
			err = fmt.Errorf("%w: %s", err, t)
		}
		p.done.finish(err)
	}()
	return p, nil
}

func (p *proc) lastLines() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.tail, " | ")
}

// stop asks the process to exit, then kills it.
func (p *proc) stop(grace time.Duration) {
	select {
	case <-p.done.Done():
		return
	default:
	}
	interrupt(p.cmd)
	select {
	case <-p.done.Done():
	case <-time.After(grace):
		kill(p.cmd)
		select {
		case <-p.done.Done():
		case <-time.After(3 * time.Second):
		}
	}
}

// run executes a short command (e.g. wg-quick up) and returns its output.
func run(ctx context.Context, argv, elevate []string, log *Log) (string, error) {
	full := append(append([]string{}, elevate...), argv...)
	log.Printf("$ %s", strings.Join(full, " "))
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		log.add(l)
	}
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", argv[0], err, strings.TrimSpace(lastN(string(out), 600)))
	}
	return string(out), nil
}

func lastN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func removeAll(dir string) {
	if dir != "" {
		os.RemoveAll(dir)
	}
}
