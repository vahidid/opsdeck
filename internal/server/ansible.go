package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opsdeck/internal/ansible"
	"opsdeck/internal/connect"
	"opsdeck/internal/vault"
	"opsdeck/internal/workspace"
)

func (s *Server) ansibleRoutes(mux *http.ServeMux, u func(http.HandlerFunc) http.HandlerFunc) {
	b := "/api/workspaces/{ws}/ansible"
	mux.HandleFunc("GET "+b, u(s.ansibleList))
	mux.HandleFunc("POST "+b+"/projects", u(s.ansibleCreate))
	mux.HandleFunc("GET "+b+"/runs", u(s.ansibleRuns))
	mux.HandleFunc("GET "+b+"/runs/{id}", u(s.ansibleRunGet))
	mux.HandleFunc("GET "+b+"/runs/{id}/log", u(s.ansibleRunLog))
	mux.HandleFunc("GET "+b+"/runs/{id}/log.txt", u(s.ansibleRunLogText))
	mux.HandleFunc("POST "+b+"/runs/{id}/cancel", u(s.ansibleRunCancel))
	p := b + "/p/{project}"
	mux.HandleFunc("GET "+p, u(s.ansibleGet))
	mux.HandleFunc("GET "+p+"/files", u(s.ansibleFiles))
	mux.HandleFunc("GET "+p+"/file", u(s.ansibleFileGet))
	mux.HandleFunc("PUT "+p+"/file", u(s.ansibleFilePut))
	mux.HandleFunc("DELETE "+p+"/file", u(s.ansibleFileDelete))
	mux.HandleFunc("POST "+p+"/rename", u(s.ansibleRename))
	mux.HandleFunc("GET "+p+"/search", u(s.ansibleSearch))
	mux.HandleFunc("POST "+p+"/unlock", u(s.ansibleUnlock))
	mux.HandleFunc("DELETE "+p+"/unlock", u(s.ansibleLock))
	mux.HandleFunc("POST "+p+"/vault/{action}", u(s.ansibleVault))
	mux.HandleFunc("GET "+p+"/inventory", u(s.ansibleInventory))
	mux.HandleFunc("GET "+p+"/inventory/host", u(s.ansibleHostVars))
	mux.HandleFunc("POST "+p+"/check", u(s.ansibleCheck))
	mux.HandleFunc("POST "+p+"/runs", u(s.ansibleRun))
}

// userCtx is the context of an action the user takes on the page.
func userCtx(r *http.Request) context.Context {
	return connect.WithActor(r.Context(), connect.Actor{Kind: "user"})
}

// ansibleFail maps a missing or wrong vault password to statuses the page
// answers with the password dialog.
func ansibleFail(w http.ResponseWriter, err error) {
	if np, ok := ansible.IsNeedPassword(err); ok {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "ansible_password", "vaultId": ansible.VaultIDName(np.VaultID)})
		return
	}
	if errors.Is(err, ansible.ErrWrongPassword) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "wrong vault password", Code: "ansible_wrong_password"})
		return
	}
	fail(w, err)
}

// vaultError turns ansible's "no vault secrets" and "decryption failed"
// errors into a request for the password of a vault id without one.
func (s *Server) vaultError(ctx context.Context, p *ansible.Project, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, "vault secrets") && !strings.Contains(msg, "Decryption failed") && !strings.Contains(msg, "vault password") {
		return err
	}
	have := s.ansible.Passwords(ctx, p)
	for _, id := range p.Summarize().VaultIDs {
		if _, ok := have[id]; !ok {
			return &ansible.NeedPassword{Project: p.Name(), VaultID: id}
		}
	}
	return &ansible.NeedPassword{Project: p.Name(), Wrong: true}
}

func (s *Server) ansibleProject(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, *ansible.Project, bool) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return nil, nil, false
	}
	p, err := ansible.Open(ws, r.PathValue("project"))
	if err != nil {
		fail(w, err)
		return nil, nil, false
	}
	return ws, p, true
}

// withPassword adds a password given with the request (used once, not kept).
func withPassword(ctx context.Context, p *ansible.Project, id, password string) context.Context {
	if password == "" {
		return ctx
	}
	return ansible.WithPassword(ctx, p, firstNonEmptyStr(id, "default"), password)
}

type projectView struct {
	ansible.Summary
	Ansible    ansible.Version `json:"ansible"`
	Remembered []string        `json:"remembered"`
}

func (s *Server) ansibleList(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	list := s.ansible.Projects(userCtx(r), ws)
	out := make([]projectView, 0, len(list))
	for _, sum := range list {
		v := projectView{Summary: sum, Remembered: []string{}}
		if p, err := ansible.Open(ws, sum.Name); err == nil {
			v.Ansible = s.ansible.Detect(r.Context(), p)
			if rem := s.ansible.Remembered(p); rem != nil {
				v.Remembered = rem
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) ansibleGet(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	sum := p.Summarize()
	for id := range s.ansible.Passwords(userCtx(r), p) {
		sum.Unlocked = append(sum.Unlocked, id)
	}
	sort.Strings(sum.Unlocked)
	v := projectView{Summary: sum, Ansible: s.ansible.Detect(r.Context(), p), Remembered: s.ansible.Remembered(p)}
	if v.Remembered == nil {
		v.Remembered = []string{}
	}
	writeJSON(w, 200, v)
}

// ansibleCreate adds a project: a new folder (with a starter layout), an
// existing folder, or a folder of a Git repository.
func (s *Server) ansibleCreate(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Name, Source, Repo, Path, Inventory, Environment, Description, Via string
		Scaffold                                                           bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	res := &workspace.Resource{Kind: ansible.Kind, Name: in.Name, Environment: in.Environment, Description: in.Description, Via: in.Via,
		Spec: map[string]any{"source": firstNonEmptyStr(in.Source, "folder"), "repo": in.Repo, "path": strings.TrimSpace(in.Path), "inventory": in.Inventory}}
	res.CleanSpec()
	if in.Source == "repo" && ws.Find("GitRemote", in.Repo) == nil {
		writeErr(w, 400, "choose a repository of the workspace")
		return
	}
	if err := ws.PutResource("", res); err != nil {
		fail(w, err)
		return
	}
	p, err := ansible.Open(ws, res.Name)
	if err == nil && !p.Exists() && p.Source == "folder" {
		if in.Scaffold {
			err = p.Scaffold()
		} else {
			err = fmt.Errorf("the folder %s does not exist", p.Root)
		}
		if err != nil {
			ws.DeleteResource(res.ID())
		}
	}
	s.record(r, ws.Name(), "ansible.project.create", res.ID(), map[string]any{"source": in.Source, "path": in.Path, "repo": in.Repo, "scaffold": in.Scaffold}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, p.Summarize())
}

func (s *Server) ansibleFiles(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	files, truncated, err := p.Files()
	if err != nil {
		fail(w, err)
		return
	}
	if files == nil {
		files = []ansible.File{}
	}
	writeJSON(w, 200, map[string]any{"files": files, "truncated": truncated})
}

func (s *Server) ansibleFileGet(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	fc, err := p.Read(r.URL.Query().Get("path"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, fc)
}

func (s *Server) ansibleFilePut(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	var in struct {
		Path, Content string
		Create        bool // fail when it exists
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	fc, err := p.Read(in.Path)
	switch {
	case err == nil && in.Create:
		writeErr(w, 409, in.Path+" already exists")
		return
	case err == nil && fc.Encrypted && !ansible.IsEncrypted([]byte(in.Content)):
		writeErr(w, 409, in.Path+" is encrypted: save it through the vault editor, or decrypt it first")
		return
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		fail(w, err)
		return
	}
	err = p.Write(in.Path, in.Content)
	s.record(r, ws.Name(), "ansible.file.write", "ansible/"+p.Name()+"/"+in.Path, map[string]any{"bytes": len(in.Content)}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) ansibleFileDelete(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	err := p.Delete(path)
	s.record(r, ws.Name(), "ansible.file.delete", "ansible/"+p.Name()+"/"+path, nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) ansibleRename(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	var in struct{ From, To string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := p.Rename(in.From, in.To)
	s.record(r, ws.Name(), "ansible.file.rename", "ansible/"+p.Name()+"/"+in.From, map[string]any{"to": in.To}, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) ansibleSearch(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	hits, err := p.Search(q.Get("q"), q.Get("regex") == "1", 500)
	if err != nil {
		fail(w, err)
		return
	}
	if hits == nil {
		hits = []ansible.Match{}
	}
	writeJSON(w, 200, hits)
}

// ansibleUnlock checks a vault password and keeps it in memory until
// opsdeck locks; with save it is also stored in the vault and referenced
// from the project.
func (s *Server) ansibleUnlock(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	var in struct {
		VaultID, Password string
		Save, AgentUse    bool
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	id := ansible.VaultIDName(strings.TrimSpace(in.VaultID))
	if in.Password == "" {
		writeErr(w, 400, "enter the password")
		return
	}
	checked, err := ansible.CheckPassword(p, id, in.Password)
	s.record(r, ws.Name(), "ansible.unlock", "ansible/"+p.Name(), map[string]any{"vaultId": id, "checked": checked, "save": in.Save}, err)
	if err != nil {
		ansibleFail(w, err)
		return
	}
	s.ansible.Remember(p, id, in.Password)
	ref := ""
	if in.Save {
		if ref, err = s.saveVaultPassword(ws, p, id, in.Password, in.AgentUse); err != nil {
			fail(w, err)
			return
		}
		s.record(r, ws.Name(), "vault.create", ref, map[string]any{"type": "login", "for": "ansible/" + p.Name()}, nil)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "checked": checked, "saved": ref})
}

// saveVaultPassword stores the password as a vault item of the workspace
// and points the project at it.
func (s *Server) saveVaultPassword(ws *workspace.Workspace, p *ansible.Project, id, password string, agentUse bool) (string, error) {
	if err := s.ensureScope(ws.Name()); err != nil {
		return "", err
	}
	base := "ansible-" + p.Name()
	if id != "default" {
		base += "-" + id
	}
	name := base
	existing, _ := s.vault.List(ws.Name())
	for n := 2; ; n++ {
		taken := false
		for _, it := range existing {
			if it.Name == name {
				taken = true
			}
		}
		if !taken {
			break
		}
		name = base + "-" + strconv.Itoa(n)
	}
	it, err := s.vault.Create(vault.Item{Scope: ws.Name(), Type: "login", Name: name, Notes: "Ansible Vault password of " + p.Name() + " (vault id " + id + ")",
		Fields: []vault.Field{{Name: "password", Value: password, Secret: true}}, Policy: vault.Policy{AgentUse: agentUse}})
	if err != nil {
		return "", err
	}
	res := *p.R
	res.Spec = map[string]any{}
	for k, v := range p.R.Spec {
		res.Spec[k] = v
	}
	if id == "default" {
		res.Spec["vaultPassword"] = it.Ref()
	} else {
		var list []any
		for _, e := range p.R.List("vaultPasswords") {
			if k, _, _ := strings.Cut(e, "="); strings.TrimSpace(k) != id {
				list = append(list, e)
			}
		}
		res.Spec["vaultPasswords"] = append(list, id+"="+it.Ref())
	}
	return it.Ref(), ws.PutResource(p.R.ID(), &res)
}

func (s *Server) ansibleLock(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	id := r.URL.Query().Get("vaultId")
	s.ansible.Forget(p, id)
	s.record(r, ws.Name(), "ansible.lock", "ansible/"+p.Name(), map[string]any{"vaultId": id}, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ansibleVault: view, save, set, encrypt, decrypt, rekey, encrypt-string.
func (s *Server) ansibleVault(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	var in struct {
		Path, Text, Key, Value, Name string
		VaultID                      string
		Password                     string // given for this action only
		NewPassword                  string
		NewVaultID                   string
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	action := r.PathValue("action")
	id := ansible.VaultIDName(strings.TrimSpace(in.VaultID))
	ctx := withPassword(userCtx(r), p, id, in.Password)
	if in.Password != "" {
		// a typed password must open the project's vault content
		if _, err := ansible.CheckPassword(p, id, in.Password); err != nil {
			ansibleFail(w, err)
			return
		}
	}
	target := "ansible/" + p.Name() + "/" + in.Path
	var out any = map[string]bool{"ok": true}
	var err error
	switch action {
	case "view":
		var text string
		if text, err = s.ansible.View(ctx, p, in.Path); err == nil {
			out = map[string]string{"text": text}
		}
	case "save":
		err = s.ansible.SaveEncrypted(ctx, p, in.Path, in.Text, id)
	case "set":
		_, err = s.ansible.SetVar(ctx, p, in.Path, in.Key, in.Value, id)
	case "encrypt":
		err = s.ansible.EncryptFile(ctx, p, in.Path, id)
	case "decrypt":
		err = s.ansible.DecryptFile(ctx, p, in.Path)
	case "rekey":
		if in.NewPassword == "" {
			err = errors.New("enter the new password")
			break
		}
		err = s.ansible.Rekey(ctx, p, in.Path, in.NewPassword, ansible.VaultIDName(firstNonEmptyStr(strings.TrimSpace(in.NewVaultID), in.VaultID)))
	case "encrypt-string":
		target = "ansible/" + p.Name()
		pw, ok := s.ansible.Passwords(ctx, p)[id]
		if !ok {
			err = &ansible.NeedPassword{Project: p.Name(), VaultID: id}
			break
		}
		vid := id
		if vid == "default" {
			vid = ""
		}
		var text string
		if text, err = ansible.EncryptString(in.Name, in.Value, pw, vid, 0); err == nil {
			out = map[string]string{"text": text}
		}
	default:
		writeErr(w, 404, "unknown vault action")
		return
	}
	if _, need := ansible.IsNeedPassword(err); !need {
		s.record(r, ws.Name(), "ansible.vault."+action, target, map[string]any{"vaultId": id}, err)
	}
	if err != nil {
		ansibleFail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type inventoryView struct {
	Inventory string              `json:"inventory"`
	Graph     string              `json:"graph"`
	Groups    map[string][]string `json:"groups"`   // group -> hosts (direct members)
	Children  map[string][]string `json:"children"` // group -> child groups
	Hosts     []string            `json:"hosts"`
	Generated string              `json:"generated,omitempty"` // the workspace inventory as written
	Error     string              `json:"error,omitempty"`
}

func (s *Server) ansibleInventory(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	inv := firstNonEmptyStr(q.Get("inventory"), p.DefaultInventory())
	ctx := withPassword(userCtx(r), p, q.Get("vaultId"), "")
	o := ansible.RunOptions{Inventory: inv}
	if inv != "" && inv != ansible.WorkspaceInventory {
		if _, err := p.Path(inv); err != nil {
			fail(w, err)
			return
		}
	}
	v := inventoryView{Inventory: inv, Groups: map[string][]string{}, Children: map[string][]string{}, Hosts: []string{}}
	if inv == ansible.WorkspaceInventory {
		var hosts []*workspace.Resource
		for _, h := range ws.Resources {
			if h.Kind == "Host" {
				hosts = append(hosts, h)
			}
		}
		if y, err := ansible.WorkspaceInventoryYAML(ws, hosts, ""); err == nil {
			v.Generated = string(y)
		}
	}
	graph, err := s.ansible.Exec(ctx, p, o, true, "ansible-inventory", "--graph")
	v.Graph = graph
	if verr := s.vaultError(ctx, p, err); verr != err {
		ansibleFail(w, verr)
		return
	}
	if err != nil {
		v.Error = err.Error()
		writeJSON(w, 200, v)
		return
	}
	list, err := s.ansible.Exec(ctx, p, o, true, "ansible-inventory", "--list", "--export")
	if err == nil {
		var raw map[string]json.RawMessage
		// output may start with warnings: take the JSON object
		if i := strings.Index(list, "{"); i >= 0 {
			list = list[i:]
		}
		if json.Unmarshal([]byte(list), &raw) == nil {
			seen := map[string]bool{}
			for g, b := range raw {
				if g == "_meta" {
					var meta struct {
						Hostvars map[string]json.RawMessage `json:"hostvars"`
					}
					json.Unmarshal(b, &meta)
					for h := range meta.Hostvars {
						seen[h] = true
					}
					continue
				}
				var grp struct {
					Hosts    []string `json:"hosts"`
					Children []string `json:"children"`
				}
				json.Unmarshal(b, &grp)
				v.Groups[g] = append([]string{}, grp.Hosts...)
				v.Children[g] = append([]string{}, grp.Children...)
				for _, h := range grp.Hosts {
					seen[h] = true
				}
			}
			for h := range seen {
				v.Hosts = append(v.Hosts, h)
			}
			sort.Strings(v.Hosts)
		}
	}
	writeJSON(w, 200, v)
}

func (s *Server) ansibleHostVars(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	inv := firstNonEmptyStr(q.Get("inventory"), p.DefaultInventory())
	host := q.Get("host")
	if host == "" || strings.HasPrefix(host, "-") {
		writeErr(w, 400, "host is required")
		return
	}
	out, err := s.ansible.Exec(userCtx(r), p, ansible.RunOptions{Inventory: inv}, true, "ansible-inventory", "--host="+host, "--yaml")
	err = s.vaultError(userCtx(r), p, err)
	if _, need := ansible.IsNeedPassword(err); !need {
		s.record(r, ws.Name(), "ansible.inventory.host", "ansible/"+p.Name(), map[string]any{"host": host, "inventory": inv}, err)
	}
	if err != nil {
		ansibleFail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"vars": out})
}

// ansibleCheck runs a quick check of a playbook that does not touch hosts.
func (s *Server) ansibleCheck(w http.ResponseWriter, r *http.Request) {
	_, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	var in struct{ Kind, Playbook, Inventory, Limit, Tags string }
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	flag := map[string]string{"syntax": "--syntax-check", "list-hosts": "--list-hosts", "list-tasks": "--list-tasks", "list-tags": "--list-tags"}[in.Kind]
	if flag == "" {
		writeErr(w, 400, "kind is syntax, list-hosts, list-tasks or list-tags")
		return
	}
	o := ansible.RunOptions{Kind: ansible.KindPlaybook, Playbook: in.Playbook, Inventory: in.Inventory, Limit: in.Limit, Tags: in.Tags}
	if err := o.Validate(p); err != nil {
		fail(w, err)
		return
	}
	args := []string{flag}
	if in.Limit != "" {
		args = append(args, "--limit="+in.Limit)
	}
	if in.Tags != "" {
		args = append(args, "--tags="+in.Tags)
	}
	args = append(args, "--", in.Playbook)
	out, err := s.ansible.Exec(userCtx(r), p, o, true, "ansible-playbook", args...)
	if verr := s.vaultError(userCtx(r), p, err); verr != err {
		ansibleFail(w, verr)
		return
	}
	res := map[string]any{"ok": err == nil, "output": out}
	if err != nil {
		res["error"] = err.Error()
	}
	writeJSON(w, 200, res)
}

// ansibleRun starts a run. It belongs to the user and keeps going when the
// page is closed; the page follows its log.
func (s *Server) ansibleRun(w http.ResponseWriter, r *http.Request) {
	ws, p, ok := s.ansibleProject(w, r)
	if !ok {
		return
	}
	var in struct {
		ansible.RunOptions
		Password string `json:"password"`
		VaultID  string `json:"vaultId"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	ctx := connect.WithActor(context.WithoutCancel(r.Context()), connect.Actor{Kind: "user"})
	if in.Password != "" {
		if _, err := ansible.CheckPassword(p, in.VaultID, in.Password); err != nil {
			ansibleFail(w, err)
			return
		}
		ctx = withPassword(ctx, p, in.VaultID, in.Password)
	}
	run, err := s.ansible.Start(ctx, p, in.RunOptions, "user")
	detail := map[string]any{"kind": in.Kind, "playbook": in.Playbook, "inventory": in.Inventory, "limit": in.Limit, "check": in.Check}
	if run != nil {
		detail["run"] = run.ID()
	}
	if _, need := ansible.IsNeedPassword(err); !need {
		s.record(r, ws.Name(), "ansible.run", "ansible/"+p.Name(), detail, err)
	}
	if err != nil {
		ansibleFail(w, err)
		return
	}
	writeJSON(w, 200, run.Info())
}

func (s *Server) ansibleRuns(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	writeJSON(w, 200, s.ansible.Runs(ws, q.Get("project"), limit))
}

func (s *Server) ansibleRunGet(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	run, err := s.ansible.Get(ws, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, run.Info())
}

func (s *Server) ansibleRunCancel(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	run, err := s.ansible.Get(ws, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	run.Cancel()
	s.record(r, ws.Name(), "ansible.run.cancel", "ansible/"+run.Info().Project, map[string]any{"run": run.ID()}, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ansibleRunLog streams a run's log as server-sent events: one {line, ts}
// per line (live runs carry timestamps, so a dropped stream resumes with
// since=<last ts>), then "status" with the run and "end".
func (s *Server) ansibleRunLog(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	run, err := s.ansible.Get(ws, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		since, _ = time.Parse(time.RFC3339Nano, v)
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	sseHeaders(w)
	flusher.Flush()
	var mu sync.Mutex
	write := func(event string, v any) error {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		_, err := fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		return err
	}
	ctx := r.Context()
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				mu.Lock()
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
				mu.Unlock()
			}
		}
	}()
	err = run.Follow(ctx, 0, func(l ansible.LogLine) error {
		if l.TS.IsZero() {
			return write("", map[string]string{"line": l.Text})
		}
		if !since.IsZero() && l.TS.Before(since) {
			return nil
		}
		return write("", map[string]string{"line": l.Text, "ts": l.TS.Format(time.RFC3339Nano)})
	})
	if err != nil || ctx.Err() != nil {
		return
	}
	write("status", run.Info())
	write("end", map[string]bool{"ok": true})
}

func (s *Server) ansibleRunLogText(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	path, err := s.ansible.LogPath(ws, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ansible-`+r.PathValue("id")+`.log"`)
	w.Write([]byte(ansible.StripANSI(string(b))))
}
