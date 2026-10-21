package connect

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"opsdeck/internal/workspace"
)

// HostKeyImport is the plan for one workspace host when importing an
// OpenSSH known_hosts file into the workspace's pinned keys.
type HostKeyImport struct {
	Host    string   `json:"host"`
	Address string   `json:"address"` // as pinned: "addr" or "[addr]:port"
	Status  string   `json:"status"`  // add | pinned | conflict | not-found
	Keys    []string `json:"keys"`    // "<type> <SHA256 fingerprint>" found in the file
	Pinned  []string `json:"pinned"`  // keys already pinned in the workspace
	add     []ssh.PublicKey
}

// PlanKnownHostsImport matches the entries of an OpenSSH known_hosts file
// (plain, wildcard or hashed host patterns) against the workspace hosts.
// A host whose pinned key is not in the file is a conflict and is never
// changed: the user has to forget the old key first.
func (h *Hub) PlanKnownHostsImport(w *workspace.Workspace, hosts []*workspace.Resource, data []byte) []HostKeyImport {
	type entry struct {
		patterns []string
		key      ssh.PublicKey
	}
	var entries []entry
	rest := data
	for len(rest) > 0 {
		marker, patterns, key, _, next, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			// skip the bad line and continue
			if i := bytes.IndexByte(rest, '\n'); i >= 0 {
				rest = rest[i+1:]
				continue
			}
			break
		}
		rest = next
		if marker != "" { // @cert-authority / @revoked are not host pins
			continue
		}
		entries = append(entries, entry{patterns, key})
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	var out []HostKeyImport
	for _, host := range hosts {
		if host.Str("address") == "" {
			continue
		}
		addr := knownhosts.Normalize(hostAddr(host))
		p := HostKeyImport{Host: host.Name, Address: addr, Keys: []string{}, Pinned: []string{}}
		seen := map[string]bool{}
		for _, e := range entries {
			if matchKnownHost(e.patterns, addr) && !seen[string(e.key.Marshal())] {
				seen[string(e.key.Marshal())] = true
				p.add = append(p.add, e.key)
				p.Keys = append(p.Keys, e.key.Type()+" "+ssh.FingerprintSHA256(e.key))
			}
		}
		pinned := h.knownKeys(w.Name(), addr)
		for _, k := range pinned {
			p.Pinned = append(p.Pinned, k.Type()+" "+ssh.FingerprintSHA256(k))
		}
		switch {
		case len(p.add) == 0:
			p.Status = "not-found"
		case len(pinned) > 0 && !containsKey(p.add, pinned[0]):
			p.Status = "conflict"
		default:
			var missing []ssh.PublicKey
			for _, k := range p.add {
				if !containsKey(pinned, k) {
					missing = append(missing, k)
				}
			}
			p.add = missing
			p.Status = "add"
			if len(missing) == 0 {
				p.Status = "pinned"
			}
		}
		out = append(out, p)
	}
	return out
}

// ApplyKnownHostsImport pins the keys planned with status "add".
func (h *Hub) ApplyKnownHostsImport(w *workspace.Workspace, plan []HostKeyImport) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	n := 0
	for _, p := range plan {
		if p.Status != "add" {
			continue
		}
		pinned := h.knownKeys(w.Name(), p.Address)
		if len(pinned) > 0 && !containsKey(p.add, pinned[0]) {
			continue // changed since the plan
		}
		for _, k := range p.add {
			if !containsKey(pinned, k) {
				fmt.Fprintf(&b, "%s %s", p.Address, ssh.MarshalAuthorizedKey(k))
				n++
			}
		}
	}
	if n == 0 {
		return 0, nil
	}
	path := h.knownHostsFile(w.Name())
	os.MkdirAll(filepath.Dir(path), 0o700)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return n, err
}

func containsKey(keys []ssh.PublicKey, k ssh.PublicKey) bool {
	for _, x := range keys {
		if bytes.Equal(x.Marshal(), k.Marshal()) {
			return true
		}
	}
	return false
}

// matchKnownHost applies OpenSSH host pattern rules: comma-separated
// patterns, * and ? wildcards, !negation, and hashed |1|salt|hash entries.
func matchKnownHost(patterns []string, addr string) bool {
	matched := false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		var ok bool
		if strings.HasPrefix(p, "|1|") {
			ok = matchHashed(p, addr)
		} else {
			ok = wildcardMatch(p, addr)
		}
		if ok && neg {
			return false
		}
		if ok {
			matched = true
		}
	}
	return matched
}

func matchHashed(p, addr string) bool {
	parts := strings.Split(p, "|") // "", "1", salt, hash
	if len(parts) != 4 {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(parts[2])
	want, err2 := base64.StdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(addr))
	return hmac.Equal(mac.Sum(nil), want)
}

// wildcardMatch matches * and ? (case-insensitive, like OpenSSH host names).
func wildcardMatch(p, s string) bool {
	p, s = strings.ToLower(p), strings.ToLower(s)
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for p = p[1:]; len(p) > 0 && p[0] == '*'; p = p[1:] {
			}
			if p == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if wildcardMatch(p, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if s == "" || p[0] != s[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return s == ""
}
