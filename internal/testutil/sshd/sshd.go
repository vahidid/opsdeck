// Package sshd is a small real SSH server for tests and local demos: exec,
// PTY shells, SFTP and direct-tcpip forwarding (so it can act as a jump host).
// Commands run as the current OS user.
package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	Addr     string
	listener net.Listener
	cfg      *ssh.ServerConfig
	Dir      string // working directory for commands
	wg       sync.WaitGroup
}

// Options select accepted credentials.
type Options struct {
	AuthorizedKeys []ssh.PublicKey
	User           string
	Password       string
	Dir            string
}

// Start listens on addr ("127.0.0.1:0" for a random port).
func Start(addr string, o Options) (*Server, error) {
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	hostKey, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range o.AuthorizedKeys {
				if string(k.Marshal()) == string(key.Marshal()) && (o.User == "" || c.User() == o.User) {
					return &ssh.Permissions{}, nil
				}
			}
			return nil, fmt.Errorf("unknown key")
		},
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if o.Password != "" && string(pw) == o.Password && (o.User == "" || c.User() == o.User) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("bad password")
		},
	}
	cfg.AddHostKey(hostKey)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{Addr: ln.Addr().String(), listener: ln, cfg: cfg, Dir: o.Dir}
	go s.serve()
	return s, nil
}

func (s *Server) Close() { s.listener.Close() }

func (s *Server) serve() {
	for {
		c, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	sc, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		c.Close()
		return
	}
	defer sc.Close()
	go func() {
		for r := range reqs {
			if r.WantReply {
				r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, reqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go s.session(ch, reqs)
		case "direct-tcpip":
			go s.directTCPIP(nc)
		default:
			nc.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

func (s *Server) directTCPIP(nc ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
		nc.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(p.Host, fmt.Sprint(p.Port)))
	if err != nil {
		nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		target.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() { io.Copy(ch, target); ch.CloseWrite(); ch.Close() }()
	go func() { io.Copy(target, ch); target.Close() }()
}

func exitStatus(ch ssh.Channel, err error) {
	code := 0
	if err != nil {
		code = 1
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				code = ws.ExitStatus()
			}
		}
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(code))
	ch.SendRequest("exit-status", false, b)
	ch.Close()
}

func (s *Server) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	var winW, winH uint32 = 80, 24
	env := os.Environ()
	for req := range reqs {
		switch req.Type {
		case "env":
			var kv struct{ K, V string }
			ssh.Unmarshal(req.Payload, &kv)
			env = append(env, kv.K+"="+kv.V)
			req.Reply(true, nil)
		case "pty-req":
			var p struct {
				Term         string
				W, H, PW, PH uint32
				Modes        string
			}
			ssh.Unmarshal(req.Payload, &p)
			winW, winH = p.W, p.H
			env = append(env, "TERM="+p.Term)
			req.Reply(true, nil)
		case "window-change":
			// before a shell starts there is no pty to resize
		case "exec":
			var p struct{ Cmd string }
			ssh.Unmarshal(req.Payload, &p)
			req.Reply(true, nil)
			cmd := exec.Command("/bin/sh", "-c", p.Cmd)
			cmd.Env, cmd.Dir = env, s.Dir
			cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
			stdin, _ := cmd.StdinPipe()
			go func() { io.Copy(stdin, ch); stdin.Close() }()
			go func() {
				err := cmd.Start()
				if err == nil {
					go func() {
						for r := range reqs {
							if r.Type == "signal" && cmd.Process != nil {
								cmd.Process.Kill()
							}
							if r.WantReply {
								r.Reply(false, nil)
							}
						}
					}()
					err = cmd.Wait()
				}
				exitStatus(ch, err)
			}()
			return
		case "shell":
			req.Reply(true, nil)
			cmd := exec.Command("/bin/bash", "-l")
			if _, err := os.Stat("/bin/bash"); err != nil {
				cmd = exec.Command("/bin/sh")
			}
			cmd.Env, cmd.Dir = env, s.Dir
			f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(winW), Rows: uint16(winH)})
			if err != nil {
				exitStatus(ch, err)
				return
			}
			go func() { io.Copy(f, ch) }()
			go func() {
				for r := range reqs {
					if r.Type == "window-change" {
						var p struct{ W, H, PW, PH uint32 }
						ssh.Unmarshal(r.Payload, &p)
						pty.Setsize(f, &pty.Winsize{Cols: uint16(p.W), Rows: uint16(p.H)})
					}
					if r.WantReply {
						r.Reply(false, nil)
					}
				}
			}()
			io.Copy(ch, f)
			exitStatus(ch, cmd.Wait())
			f.Close()
			return
		case "subsystem":
			var p struct{ Name string }
			ssh.Unmarshal(req.Payload, &p)
			if p.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			srv, err := sftp.NewServer(ch)
			if err != nil {
				ch.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			if err := srv.Serve(); err != nil && err != io.EOF {
				log.Printf("sftp: %v", err)
			}
			srv.Close()
			ch.Close()
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}
