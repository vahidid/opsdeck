// Package audit is an append-only, hash-chained journal of every action taken
// through opsdeck (by the user or by an agent).
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one journal record. Hash = sha256(prevHash + canonical JSON of the
// entry without Hash), so editing or removing a past line breaks the chain.
type Entry struct {
	Seq       int64          `json:"seq"`
	TS        time.Time      `json:"ts"`
	Workspace string         `json:"workspace,omitempty"`
	Actor     string         `json:"actor"` // "user", "agent:<session>", "system"
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
	Result    string         `json:"result"` // "ok", "error", "denied", ...
	PrevHash  string         `json:"prevHash"`
	Hash      string         `json:"hash"`
}

// Log is safe for concurrent use.
type Log struct {
	mu      sync.Mutex
	f       *os.File
	entries []Entry
}

func Open(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "audit.jsonl")
	l := &Log{}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.entries = append(l.entries, e)
			}
		}
		f.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	l.f = f
	return l, nil
}

func (l *Log) Close() error { return l.f.Close() }

func hashEntry(e Entry) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(append([]byte(e.PrevHash), b...))
	return hex.EncodeToString(sum[:])
}

// Record appends an entry and returns it.
func (l *Log) Record(e Entry) Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.TS = time.Now().UTC().Truncate(time.Millisecond)
	if n := len(l.entries); n > 0 {
		e.Seq = l.entries[n-1].Seq + 1
		e.PrevHash = l.entries[n-1].Hash
	} else {
		e.Seq = 1
	}
	if e.Result == "" {
		e.Result = "ok"
	}
	e.Hash = hashEntry(e)
	b, _ := json.Marshal(e)
	l.f.Write(append(b, '\n'))
	l.entries = append(l.entries, e)
	return e
}

// Query returns entries newest first. before=0 means from the newest.
func (l *Log) Query(workspace string, before int64, limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	out := []Entry{}
	for i := len(l.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.entries[i]
		if before > 0 && e.Seq >= before {
			continue
		}
		if workspace != "" && e.Workspace != workspace {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Verify recomputes the chain. brokenAt is the first bad sequence number.
func (l *Log) Verify() (ok bool, count int, brokenAt int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := ""
	for _, e := range l.entries {
		if e.PrevHash != prev || hashEntry(e) != e.Hash {
			return false, len(l.entries), e.Seq
		}
		prev = e.Hash
	}
	return true, len(l.entries), 0
}
