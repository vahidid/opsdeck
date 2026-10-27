package vpn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/sysproc"
)

// proc is a VPN client subprocess. Its output goes to the tunnel log and to
// onLine; it is stopped with SIGINT/SIGTERM (relayed by sudo to the client)
// and killed if it does not exit.
type proc struct {
	cmd   *exec.Cmd
	done  *doneOnce
	log   *Log
	stdin io.WriteCloser // open for answers (startProcPrompts)
	mu    sync.Mutex
	tail  []string // last lines, for error messages
	sudo  string   // why sudo could not start the client ("" = it did)
}

// promptIdle is how long output waits without a newline before it counts as
// a question (openconnect's prompts end without one).
const promptIdle = 300 * time.Millisecond

// startProc runs argv (prefixed with elevate when non-empty) with stdin.
func startProc(argv, elevate []string, stdin string, log *Log, onLine func(string)) (*proc, error) {
	return startProcPrompts(argv, elevate, stdin, log, onLine, nil)
}

// startProcPrompts is startProc for a client that asks questions when
// onPrompt is set: stdin stays open after stdin for the answers
// (proc.answer), and output that then waits without a newline is a
// question, which goes to the log and to onPrompt (not to onLine).
func startProcPrompts(argv, elevate []string, stdin string, log *Log, onLine, onPrompt func(string)) (*proc, error) {
	full, env := elevatedCommand(argv, elevate)
	cmd := sysproc.Command(full[0], full[1:]...)
	cmd.Env = env
	setProcAttrs(cmd, len(elevate) > 0)
	p := &proc{cmd: cmd, done: newDone(), log: log}
	if onPrompt != nil {
		w, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		p.stdin = w
	} else if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	log.Printf("$ %s", strings.Join(full, " "))
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	if p.stdin != nil && stdin != "" {
		io.WriteString(p.stdin, stdin) // small: fits in the pipe
	}
	emit := func(line string, prompt bool) {
		log.add(line)
		p.mu.Lock()
		p.tail = append(p.tail, line)
		if len(p.tail) > 8 {
			p.tail = p.tail[1:]
		}
		if h := sudoHint(line); h != "" && p.sudo == "" {
			p.sudo = h
		}
		p.mu.Unlock()
		switch {
		case prompt:
			onPrompt(strings.TrimSpace(line))
		case onLine != nil:
			onLine(line)
		}
	}
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 32*1024)
			n, err := pr.Read(buf)
			if n > 0 {
				chunks <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		var part []byte
		var idle <-chan time.Time
		for {
			select {
			case b, ok := <-chunks:
				if !ok {
					if len(part) > 0 {
						emit(string(part), false)
					}
					return
				}
				part = append(part, b...)
				for {
					i := bytes.IndexByte(part, '\n')
					if i < 0 {
						break
					}
					emit(strings.TrimRight(string(part[:i]), "\r"), false)
					part = part[i+1:]
				}
				if len(part) > 1<<20 {
					emit(string(part), false)
					part = nil
				}
				idle = nil
				if len(part) > 0 && onPrompt != nil {
					idle = time.After(promptIdle)
				}
			case <-idle:
				idle = nil
				q := string(part)
				part = nil
				emit(q, true)
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
		p.mu.Lock()
		hint := p.sudo
		p.mu.Unlock()
		if hint != "" {
			err = errors.New(hint)
		} else if t := p.lastLines(); t != "" {
			err = fmt.Errorf("%w: %s", err, t)
		}
		p.done.finish(err)
	}()
	return p, nil
}

// answer writes an answer to a question of the client.
func (p *proc) answer(s string) error {
	if p.stdin == nil {
		return errors.New("the client takes no answers")
	}
	_, err := io.WriteString(p.stdin, s+"\n")
	return err
}

// noAnswer closes the client's stdin: it gives up on its question.
func (p *proc) noAnswer() {
	if p.stdin != nil {
		p.stdin.Close()
	}
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
	full, env := elevatedCommand(argv, elevate)
	log.Printf("$ %s", strings.Join(full, " "))
	cmd := sysproc.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = env
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	hint := ""
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		log.add(l)
		if h := sudoHint(l); h != "" && hint == "" {
			hint = h
		}
	}
	if err != nil && hint != "" {
		return string(out), errors.New(hint)
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
