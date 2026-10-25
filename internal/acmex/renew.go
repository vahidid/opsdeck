package acmex

import (
	"context"
	"log"
	"os"
	"time"

	"opsdeck/internal/workspace"
)

func fsMode(m uint32) os.FileMode { return os.FileMode(m) }

// Renewals renews, while the vault is unlocked, every due certificate whose
// resource asks for it (autoRenew), checking every interval (the first time
// after a short delay). It returns when ctx is done.
func (m *Manager) Renewals(ctx context.Context, workspaces func() []*workspace.Workspace, interval time.Duration) {
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	first := time.NewTimer(90 * time.Second)
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	for {
		m.RenewDue(workspaces())
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// RenewDue starts a renewal for every due certificate with autoRenew and
// returns the jobs started.
func (m *Manager) RenewDue(wss []*workspace.Workspace) []*Job {
	if m.Vault == nil || !m.Vault.Unlocked() {
		return nil
	}
	var started []*Job
	for _, ws := range wss {
		for _, r := range ws.Resources {
			if r.Kind != "Certificate" || !r.Bool("autoRenew", false) {
				continue
			}
			st := m.Status(ws, r)
			if st.Error != "" || !st.Due || st.Job != "" {
				continue
			}
			j, err := m.Start(ws, r, "system")
			if err != nil {
				log.Printf("certificate %s/%s: renewal not started: %v", ws.Name(), r.Name, err)
				continue
			}
			log.Printf("certificate %s/%s: renewing (%s), job %s", ws.Name(), r.Name, st.Reason, j.ID)
			started = append(started, j)
		}
	}
	return started
}
