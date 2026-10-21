// Package vault implements the local encrypted secret store.
//
// Key hierarchy: password --Argon2id--> KEK --wraps--> Vault Key --wraps--> one
// key per scope ("global" or a workspace name) --encrypts--> items.
// Everything about an item except its id, scope, revision and timestamps is
// encrypted, including its name.
package vault

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const formatVersion = "1"

// GlobalScope holds personal items shared across workspaces.
const GlobalScope = "global"

var (
	ErrLocked         = errors.New("vault is locked")
	ErrNotInitialized = errors.New("vault is not initialized")
	ErrInitialized    = errors.New("vault is already initialized")
	ErrBadPassword    = errors.New("wrong master password")
	ErrNotFound       = errors.New("vault item not found")
	ErrConflict       = errors.New("an item with this name already exists in the scope")
)

// Vault is safe for concurrent use.
type Vault struct {
	mu        sync.RWMutex
	db        *sql.DB
	vaultID   string
	vk        []byte            // nil while locked
	scopeKeys map[string][]byte // decrypted scope keys
	items     map[string]*Item  // decrypted items by id

	lastUse   time.Time
	autoLock  time.Duration
	onLock    []func()
	stop      chan struct{}
	closeOnce sync.Once
}

// Open opens (or creates) the vault database file.
func Open(path string) (*Vault, error) {
	// Owner-only file; SQLite gives the -wal/-shm files the same mode.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		f.Close()
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		os.Chmod(p, 0o600)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS scopes (id TEXT PRIMARY KEY, wrapped_key BLOB NOT NULL, created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS items (
  id TEXT PRIMARY KEY, scope TEXT NOT NULL REFERENCES scopes(id),
  revision INTEGER NOT NULL, created INTEGER NOT NULL, updated INTEGER NOT NULL, blob BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS item_history (
  id TEXT NOT NULL, scope TEXT NOT NULL, revision INTEGER NOT NULL, archived INTEGER NOT NULL, blob BLOB NOT NULL,
  PRIMARY KEY (id, revision));`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("vault schema: %w", err)
	}
	v := &Vault{db: db, autoLock: 15 * time.Minute, stop: make(chan struct{})}
	var id []byte
	if err := db.QueryRow(`SELECT v FROM meta WHERE k='vault_id'`).Scan(&id); err == nil {
		v.vaultID = string(id)
	}
	go v.autoLockLoop()
	return v, nil
}

// Close locks the vault and closes the database.
func (v *Vault) Close() error {
	var err error
	v.closeOnce.Do(func() {
		close(v.stop)
		v.Lock()
		err = v.db.Close()
	})
	return err
}

func (v *Vault) Initialized() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.vaultID != ""
}

func (v *Vault) Unlocked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.vk != nil
}

// SetAutoLock sets the idle timeout; 0 disables auto-lock.
func (v *Vault) SetAutoLock(d time.Duration) {
	v.mu.Lock()
	v.autoLock = d
	v.mu.Unlock()
}

func (v *Vault) AutoLock() time.Duration {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.autoLock
}

// Touch records activity so the vault does not auto-lock.
func (v *Vault) Touch() {
	v.mu.Lock()
	v.lastUse = time.Now()
	v.mu.Unlock()
}

// OnLock registers a callback run (outside the vault lock) after locking.
func (v *Vault) OnLock(fn func()) {
	v.mu.Lock()
	v.onLock = append(v.onLock, fn)
	v.mu.Unlock()
}

func (v *Vault) autoLockLoop() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-v.stop:
			return
		case <-t.C:
			v.mu.RLock()
			expired := v.vk != nil && v.autoLock > 0 && time.Since(v.lastUse) > v.autoLock
			v.mu.RUnlock()
			if expired {
				v.Lock()
			}
		}
	}
}

// Init creates a new vault protected by password and leaves it unlocked.
func (v *Vault) Init(password string) error {
	if len(password) < 8 {
		return errors.New("master password must be at least 8 characters")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vaultID != "" {
		return ErrInitialized
	}
	kdf, err := defaultKDF()
	if err != nil {
		return err
	}
	kek, err := kdf.derive(password)
	if err != nil {
		return err
	}
	defer wipe(kek)
	vk, err := randomBytes(32)
	if err != nil {
		return err
	}
	vaultID := uuid.NewString()
	wrapped, err := seal(kek, vk, aadVK(vaultID))
	if err != nil {
		return err
	}
	kdfJSON, _ := json.Marshal(kdf)
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, val := range map[string][]byte{
		"format_version": []byte(formatVersion), "vault_id": []byte(vaultID),
		"kdf": kdfJSON, "wrapped_vk": wrapped,
	} {
		if _, err := tx.Exec(`INSERT INTO meta (k, v) VALUES (?, ?)`, k, val); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v.vaultID = vaultID
	v.vk = vk
	v.scopeKeys = map[string][]byte{}
	v.items = map[string]*Item{}
	v.lastUse = time.Now()
	return v.ensureScopeLocked(GlobalScope)
}

// Unlock derives the KEK from password and decrypts all scopes and items.
func (v *Vault) Unlock(password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vaultID == "" {
		return ErrNotInitialized
	}
	if v.vk != nil {
		v.lastUse = time.Now()
		return nil
	}
	kdf, wrapped, err := v.header()
	if err != nil {
		return err
	}
	kek, err := kdf.derive(password)
	if err != nil {
		return err
	}
	defer wipe(kek)
	vk, err := open(kek, wrapped, aadVK(v.vaultID))
	if err != nil {
		return ErrBadPassword
	}
	scopeKeys, items, err := v.loadAll(vk)
	if err != nil {
		wipe(vk)
		return err
	}
	v.vk, v.scopeKeys, v.items = vk, scopeKeys, items
	v.lastUse = time.Now()
	return nil
}

func (v *Vault) header() (KDFParams, []byte, error) {
	var kdfJSON, wrapped []byte
	if err := v.db.QueryRow(`SELECT v FROM meta WHERE k='kdf'`).Scan(&kdfJSON); err != nil {
		return KDFParams{}, nil, err
	}
	if err := v.db.QueryRow(`SELECT v FROM meta WHERE k='wrapped_vk'`).Scan(&wrapped); err != nil {
		return KDFParams{}, nil, err
	}
	var kdf KDFParams
	if err := json.Unmarshal(kdfJSON, &kdf); err != nil {
		return KDFParams{}, nil, err
	}
	return kdf, wrapped, nil
}

func (v *Vault) loadAll(vk []byte) (map[string][]byte, map[string]*Item, error) {
	scopeKeys := map[string][]byte{}
	rows, err := v.db.Query(`SELECT id, wrapped_key FROM scopes`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		var wrapped []byte
		if err := rows.Scan(&id, &wrapped); err != nil {
			rows.Close()
			return nil, nil, err
		}
		key, err := open(vk, wrapped, aadScope(v.vaultID, id))
		if err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scope %q: %w", id, err)
		}
		scopeKeys[id] = key
	}
	rows.Close()

	items := map[string]*Item{}
	rows, err = v.db.Query(`SELECT id, scope, revision, created, updated, blob FROM items`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, scope string
		var rev int
		var created, updated int64
		var blob []byte
		if err := rows.Scan(&id, &scope, &rev, &created, &updated, &blob); err != nil {
			return nil, nil, err
		}
		key := scopeKeys[scope]
		if key == nil {
			return nil, nil, fmt.Errorf("item %s: missing scope key %q", id, scope)
		}
		pt, err := open(key, blob, aadItem(v.vaultID, scope, id, rev))
		if err != nil {
			return nil, nil, fmt.Errorf("item %s: %w", id, err)
		}
		var it Item
		err = json.Unmarshal(pt, &it)
		wipe(pt)
		if err != nil {
			return nil, nil, fmt.Errorf("item %s: %w", id, err)
		}
		it.ID, it.Scope, it.Revision = id, scope, rev
		it.Created, it.Updated = time.UnixMilli(created), time.UnixMilli(updated)
		items[id] = &it
	}
	return scopeKeys, items, rows.Err()
}

// Lock wipes all key material from memory.
func (v *Vault) Lock() {
	v.mu.Lock()
	if v.vk == nil {
		v.mu.Unlock()
		return
	}
	wipe(v.vk)
	for _, k := range v.scopeKeys {
		wipe(k)
	}
	for _, it := range v.items {
		for i := range it.Fields {
			it.Fields[i].Value = ""
		}
	}
	v.vk, v.scopeKeys, v.items = nil, nil, nil
	callbacks := append([]func(){}, v.onLock...)
	v.mu.Unlock()
	for _, fn := range callbacks {
		fn()
	}
}

// ChangePassword re-wraps the vault key under a new password.
func (v *Vault) ChangePassword(oldPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("master password must be at least 8 characters")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return ErrLocked
	}
	kdf, wrapped, err := v.header()
	if err != nil {
		return err
	}
	oldKEK, err := kdf.derive(oldPassword)
	if err != nil {
		return err
	}
	defer wipe(oldKEK)
	if _, err := open(oldKEK, wrapped, aadVK(v.vaultID)); err != nil {
		return ErrBadPassword
	}
	newKDF, err := defaultKDF()
	if err != nil {
		return err
	}
	newKEK, err := newKDF.derive(newPassword)
	if err != nil {
		return err
	}
	defer wipe(newKEK)
	newWrapped, err := seal(newKEK, v.vk, aadVK(v.vaultID))
	if err != nil {
		return err
	}
	kdfJSON, _ := json.Marshal(newKDF)
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE meta SET v=? WHERE k='kdf'`, kdfJSON); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE meta SET v=? WHERE k='wrapped_vk'`, newWrapped); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsureScope creates a scope (with its own key) if it does not exist.
func (v *Vault) EnsureScope(scope string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return ErrLocked
	}
	return v.ensureScopeLocked(scope)
}

func (v *Vault) ensureScopeLocked(scope string) error {
	if _, ok := v.scopeKeys[scope]; ok {
		return nil
	}
	key, err := randomBytes(32)
	if err != nil {
		return err
	}
	wrapped, err := seal(v.vk, key, aadScope(v.vaultID, scope))
	if err != nil {
		return err
	}
	if _, err := v.db.Exec(`INSERT INTO scopes (id, wrapped_key, created) VALUES (?, ?, ?)`,
		scope, wrapped, time.Now().UnixMilli()); err != nil {
		return err
	}
	v.scopeKeys[scope] = key
	return nil
}

// DeleteScope removes a scope and every item in it (including history).
func (v *Vault) DeleteScope(scope string) error {
	if scope == GlobalScope {
		return errors.New("the global scope cannot be deleted")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return ErrLocked
	}
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM item_history WHERE scope=?`, `DELETE FROM items WHERE scope=?`, `DELETE FROM scopes WHERE id=?`} {
		if _, err := tx.Exec(q, scope); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for id, it := range v.items {
		if it.Scope == scope {
			delete(v.items, id)
		}
	}
	if k := v.scopeKeys[scope]; k != nil {
		wipe(k)
		delete(v.scopeKeys, scope)
	}
	return nil
}

// List returns copies of all items in a scope, sorted by name.
func (v *Vault) List(scope string) ([]Item, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.vk == nil {
		return nil, ErrLocked
	}
	out := []Item{}
	for _, it := range v.items {
		if scope == "" || it.Scope == scope {
			out = append(out, it.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// Get returns a copy of one item.
func (v *Vault) Get(id string) (Item, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.vk == nil {
		return Item{}, ErrLocked
	}
	it, ok := v.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	return it.clone(), nil
}

// Resolve finds the item a vault:// reference points to. It returns the item
// and the referenced field name ("" when the whole item is referenced).
func (v *Vault) Resolve(ref string) (Item, string, error) {
	scope, name, field, err := ParseRef(ref)
	if err != nil {
		return Item{}, "", err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.vk == nil {
		return Item{}, "", ErrLocked
	}
	for _, it := range v.items {
		if it.Scope == scope && it.Name == name {
			if field != "" && !hasField(it, field) {
				return Item{}, "", fmt.Errorf("%s: no field %q", ref, field)
			}
			return it.clone(), field, nil
		}
	}
	return Item{}, "", fmt.Errorf("%w: %s", ErrNotFound, ref)
}

// ErrScope is returned when a reference points outside the allowed scopes.
var ErrScope = errors.New("vault reference outside this workspace's scopes")

// ResolveFor resolves a reference on behalf of a workspace: only that
// workspace's scope and the global scope are reachable. ws == "" allows only
// the global scope (app-level settings).
func (v *Vault) ResolveFor(ws, ref string) (Item, string, error) {
	scope, _, _, err := ParseRef(ref)
	if err != nil {
		return Item{}, "", err
	}
	if scope != GlobalScope && (ws == "" || scope != ws) {
		return Item{}, "", fmt.Errorf("%w: %s", ErrScope, ref)
	}
	return v.Resolve(ref)
}

func hasField(it *Item, name string) bool {
	for _, f := range it.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// Create stores a new item.
func (v *Vault) Create(in Item) (Item, error) {
	if err := normalize(&in); err != nil {
		return Item{}, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return Item{}, ErrLocked
	}
	if _, ok := v.scopeKeys[in.Scope]; !ok {
		return Item{}, fmt.Errorf("unknown scope %q", in.Scope)
	}
	if v.nameTaken(in.Scope, in.Name, "") {
		return Item{}, ErrConflict
	}
	now := time.Now().Truncate(time.Millisecond)
	in.ID = uuid.NewString()
	in.Revision = 1
	in.Created, in.Updated = now, now
	if err := v.write(&in, false); err != nil {
		return Item{}, err
	}
	v.items[in.ID] = &in
	v.lastUse = time.Now()
	return in.clone(), nil
}

// Update replaces an item's content. Secret fields sent with keep=true in
// keepSecrets keep their stored value (the UI never sees them).
func (v *Vault) Update(id string, in Item, keepSecrets map[string]bool) (Item, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return Item{}, ErrLocked
	}
	cur, ok := v.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	in.ID, in.Scope, in.Type = cur.ID, cur.Scope, cur.Type
	for i, f := range in.Fields {
		if keepSecrets[f.Name] {
			// Keeping a value keeps its secrecy too: a field cannot be
			// turned into a plain (listed, unredacted) one without re-entering it.
			for _, old := range cur.Fields {
				if old.Name == f.Name {
					in.Fields[i].Value, in.Fields[i].Secret = old.Value, old.Secret
				}
			}
		}
	}
	if err := normalize(&in); err != nil {
		return Item{}, err
	}
	if v.nameTaken(in.Scope, in.Name, id) {
		return Item{}, ErrConflict
	}
	in.Created = cur.Created
	in.Updated = time.Now().Truncate(time.Millisecond)
	in.Revision = cur.Revision + 1
	if err := v.write(&in, true); err != nil {
		return Item{}, err
	}
	v.items[id] = &in
	v.lastUse = time.Now()
	return in.clone(), nil
}

// Delete removes an item (its history is kept until the scope is deleted).
func (v *Vault) Delete(id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vk == nil {
		return ErrLocked
	}
	if _, ok := v.items[id]; !ok {
		return ErrNotFound
	}
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR REPLACE INTO item_history (id, scope, revision, archived, blob)
		SELECT id, scope, revision, ?, blob FROM items WHERE id=?`, time.Now().UnixMilli(), id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE id=?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	delete(v.items, id)
	return nil
}

func (v *Vault) nameTaken(scope, name, exceptID string) bool {
	for _, it := range v.items {
		if it.Scope == scope && it.ID != exceptID && strings.EqualFold(it.Name, name) {
			return true
		}
	}
	return false
}

// write encrypts and persists an item; the previous revision moves to history.
func (v *Vault) write(it *Item, update bool) error {
	payload := struct {
		Type   string   `json:"type"`
		Name   string   `json:"name"`
		Tags   []string `json:"tags"`
		Notes  string   `json:"notes"`
		Fields []Field  `json:"fields"`
		Policy Policy   `json:"policy"`
	}{it.Type, it.Name, it.Tags, it.Notes, it.Fields, it.Policy}
	pt, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	defer wipe(pt)
	blob, err := seal(v.scopeKeys[it.Scope], pt, aadItem(v.vaultID, it.Scope, it.ID, it.Revision))
	if err != nil {
		return err
	}
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if update {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO item_history (id, scope, revision, archived, blob)
			SELECT id, scope, revision, ?, blob FROM items WHERE id=?`, time.Now().UnixMilli(), it.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO items (id, scope, revision, created, updated, blob) VALUES (?, ?, ?, ?, ?, ?)`,
		it.ID, it.Scope, it.Revision, it.Created.UnixMilli(), it.Updated.UnixMilli(), blob); err != nil {
		return err
	}
	return tx.Commit()
}

// SecretValues returns every secret value currently known, labelled with the
// reference it belongs to. Used to redact secrets from model input.
func (v *Vault) SecretValues() map[string]string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := map[string]string{}
	for _, it := range v.items {
		for _, f := range it.Fields {
			if f.Secret && len(f.Value) >= 6 {
				out[f.Value] = it.Ref() + "#" + f.Name
			}
		}
	}
	return out
}
