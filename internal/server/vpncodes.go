package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"opsdeck/internal/config"
	"opsdeck/internal/connect"
	"opsdeck/internal/vpn"
)

// One-time codes for VPN logins (FortiGate codes by SMS or email): the user
// enters them in the app, or they arrive by themselves from the sources in
// App settings → VPN (see internal/vpn/codes.go).

// codeSources builds the automatic code sources of the settings; secret
// resolves the vault reference of the ntfy access token.
func codeSources(c config.VPNCodes, secret func(ref string) (string, error)) ([]vpn.CodeSource, error) {
	re, err := vpn.CompileCodePattern(c.Pattern)
	if err != nil {
		return nil, err
	}
	var out []vpn.CodeSource
	var errs []error
	if u := strings.TrimSpace(c.NtfyURL); u != "" {
		src := &vpn.NtfySource{URL: u, Pattern: re}
		if c.NtfyTokenRef != "" {
			if src.Token, err = secret(c.NtfyTokenRef); err != nil {
				errs = append(errs, fmt.Errorf("the ntfy access token: %w", err))
			}
		}
		out = append(out, src)
	}
	if strings.TrimSpace(c.Command) != "" {
		out = append(out, &vpn.CommandSource{Command: c.Command, Pattern: re})
	}
	return out, errors.Join(errs...)
}

// checkVPNCodes validates (and tidies) the code sources of the settings.
func checkVPNCodes(c *config.VPNCodes) error {
	c.NtfyURL, c.Command = strings.TrimSpace(c.NtfyURL), strings.TrimSpace(c.Command)
	if c.NtfyURL != "" {
		u, err := url.Parse(c.NtfyURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.Trim(u.Path, "/") == "" || u.RawQuery != "" {
			return fmt.Errorf("the ntfy topic must be a URL like https://ntfy.sh/<topic>")
		}
		c.NtfyURL = strings.TrimRight(c.NtfyURL, "/")
	}
	if c.NtfyTokenRef != "" && !strings.HasPrefix(c.NtfyTokenRef, "vault://") {
		return fmt.Errorf("the ntfy access token must be a vault:// reference")
	}
	_, err := vpn.CompileCodePattern(c.Pattern)
	return err
}

// netCode answers a network's request for a one-time code.
func (s *Server) netCode(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.workspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	err := s.hub.NetworkCode(ws.Name(), r.PathValue("name"), in.Code)
	s.record(r, ws.Name(), "network.code", "network/"+r.PathValue("name"), nil, err)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, s.hub.Networks(ws))
}

// vpnCodes lists the networks of every workspace that wait for a code, for
// the prompt the app shows on any page.
func (s *Server) vpnCodes(w http.ResponseWriter, r *http.Request) {
	if !s.vault.Unlocked() {
		writeJSON(w, 200, []connect.PendingCode{})
		return
	}
	writeJSON(w, 200, s.hub.PendingCodes())
}

// vpnCodesTest waits for a message with a code from the posted (not yet
// saved) sources, so the forwarding from the phone can be tried out.
func (s *Server) vpnCodesTest(w http.ResponseWriter, r *http.Request) {
	var in config.VPNCodes
	if err := readJSON(r, &in); err != nil {
		fail(w, err)
		return
	}
	if err := checkVPNCodes(&in); err != nil {
		fail(w, err)
		return
	}
	srcs, err := codeSources(in, s.providerKey)
	if err != nil {
		fail(w, err)
		return
	}
	if len(srcs) == 0 {
		fail(w, errors.New("set an ntfy topic or a command first"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	type result struct {
		code, source string
		err          error
	}
	results := make(chan result, len(srcs))
	p := vpn.CodePrompt{Network: "test", Message: "a test from App settings", Since: time.Now()}
	for _, src := range srcs {
		go func() {
			code, err := src.Wait(ctx, p)
			results <- result{code, src.Name(), err}
		}()
	}
	var errs []error
	for range srcs {
		res := <-results
		if res.code != "" {
			writeJSON(w, 200, map[string]string{"code": res.code, "source": res.source})
			return
		}
		if ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("%s: %w", res.source, res.err))
		}
	}
	if len(errs) == 0 {
		errs = append(errs, errors.New("no message with a code arrived within 90 seconds"))
	}
	fail(w, errors.Join(errs...))
}
