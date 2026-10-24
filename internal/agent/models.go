package agent

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	"opsdeck/internal/config"
	"opsdeck/internal/workspace"
)

// ModelAllowed reports whether a workspace lets its sessions use a model
// (settings.models: patterns such as ollama/* or anthropic/claude-*).
func ModelAllowed(w *workspace.Workspace, id string) bool {
	if len(w.Settings.Models) == 0 {
		return true
	}
	for _, p := range w.Settings.Models {
		if p = strings.TrimSpace(p); p == id {
			return true
		}
		if ok, _ := path.Match(p, id); ok {
			return true
		}
	}
	return false
}

// checkModel refuses a model the workspace does not allow.
func checkModel(w *workspace.Workspace, id string) error {
	if id == "" || ModelAllowed(w, id) {
		return nil
	}
	return fmt.Errorf("workspace %s only allows the models %s (Workspace settings → Agent); %s is not one of them", w.Name(), strings.Join(w.Settings.Models, ", "), id)
}

// localModelsOnly reports whether every model the workspace allows runs on
// this computer (an OpenAI-compatible server on a loopback address).
func (m *Manager) localModelsOnly(w *workspace.Workspace) bool {
	if len(w.Settings.Models) == 0 || m.deps.Router == nil {
		return false
	}
	router := m.deps.Router()
	for _, p := range w.Settings.Models {
		pid, _, _ := strings.Cut(strings.TrimSpace(p), "/")
		if pid == "" || strings.ContainsAny(pid, "*?[") {
			return false
		}
		prov, _, ok := router.Lookup(pid + "/x")
		if !ok || !isLocalProvider(prov) {
			return false
		}
	}
	return true
}

func isLocalProvider(p config.Provider) bool {
	if p.Type != "openai" {
		return false
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
