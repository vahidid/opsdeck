// Package bundle moves a workspace to another machine or person: its files
// (inventory, docs, skills, scripts…) and its vault scope go into one file
// encrypted with a password. Importing opens it once and stores the secrets
// in the receiving vault under that vault's own keys.
//
// Not included: agent sessions, reports, artifacts, Git working copies and
// other local state, and items of the global vault scope.
package bundle

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

const (
	Format  = "opsdeck-workspace"
	Version = 1
)

var aad = []byte("opsdeck/workspace-export|1")

// File is the exported file (JSON). Only the format, the creation time and
// the key derivation parameters are readable without the password.
type File struct {
	Format  string    `json:"format"`
	Version int       `json:"version"`
	Created time.Time `json:"created"`
	vault.Sealed
}

// content is the encrypted part.
type content struct {
	Name  string            `json:"name"`
	Files map[string][]byte `json:"files"`
	Items []item            `json:"items"`
}

type item struct {
	Type   string        `json:"type"`
	Name   string        `json:"name"`
	Tags   []string      `json:"tags"`
	Notes  string        `json:"notes"`
	Fields []vault.Field `json:"fields"`
	Policy vault.Policy  `json:"policy"`
}

// Export packs a workspace and its vault scope, encrypted with password.
func Export(ws *workspace.Workspace, v *vault.Vault, password string) (*File, int, error) {
	files, err := ws.Files()
	if err != nil {
		return nil, 0, err
	}
	items, err := v.List(ws.Name())
	if err != nil {
		return nil, 0, err
	}
	c := content{Name: ws.Name(), Files: files, Items: []item{}}
	for _, it := range items {
		c.Items = append(c.Items, item{it.Type, it.Name, it.Tags, it.Notes, it.Fields, it.Policy})
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(c); err != nil {
		return nil, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	sealed, err := vault.SealWithPassword(password, buf.Bytes(), aad)
	if err != nil {
		return nil, 0, err
	}
	return &File{Format: Format, Version: Version, Created: time.Now().UTC().Truncate(time.Second), Sealed: sealed}, len(c.Items), nil
}

// Result is what an import created.
type Result struct {
	Workspace *workspace.Workspace
	Items     int // vault items added
	Skipped   int // items kept because the scope already had one with that name
}

// Import creates the workspace and its vault items from an exported file.
func Import(m *workspace.Manager, v *vault.Vault, f *File, password string) (*Result, error) {
	if f.Format != Format {
		return nil, errors.New("not an opsdeck workspace export")
	}
	if f.Version != Version {
		return nil, fmt.Errorf("export format version %d is not supported by this opsdeck", f.Version)
	}
	pt, err := f.Open(password, aad)
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(pt))
	if err != nil {
		return nil, err
	}
	var c content
	if err := json.NewDecoder(io.LimitReader(zr, 512<<20)).Decode(&c); err != nil {
		return nil, fmt.Errorf("reading the export: %w", err)
	}
	if !v.Unlocked() {
		return nil, vault.ErrLocked
	}
	ws, err := m.CreateFromFiles(c.Name, c.Files)
	if err != nil {
		return nil, err
	}
	res := &Result{Workspace: ws}
	var created []string
	err = v.EnsureScope(ws.Name())
	for _, it := range c.Items {
		if err != nil {
			break
		}
		var stored vault.Item
		stored, err = v.Create(vault.Item{Scope: ws.Name(), Type: it.Type, Name: it.Name, Tags: it.Tags, Notes: it.Notes, Fields: it.Fields, Policy: it.Policy})
		switch {
		case errors.Is(err, vault.ErrConflict):
			// left over from a deleted workspace of the same name: keep it
			res.Skipped++
			err = nil
		case err == nil:
			created = append(created, stored.ID)
		default:
			err = fmt.Errorf("vault item %q: %w", it.Name, err)
		}
	}
	if err != nil {
		for _, id := range created {
			v.Delete(id)
		}
		m.Delete(ws.Name())
		return nil, err
	}
	res.Items = len(created)
	return res, nil
}
