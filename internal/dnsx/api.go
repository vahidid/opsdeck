package dnsx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// api is a JSON API client with the connector's authentication.
type api struct {
	base string
	hc   *http.Client
	auth func(*http.Request)
}

// do sends a request and decodes the JSON answer into out (which may be
// nil). It returns the status code; a status of 300 or more is an error
// carrying the provider's message when one can be read.
func (a *api) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	u := a.base + path
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opsdeck")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	a.auth(req)
	res, err := a.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode >= 300 {
		return res.StatusCode, apiError(res.StatusCode, b)
	}
	if out != nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return res.StatusCode, fmt.Errorf("unexpected answer from %s: %v", u, err)
		}
	}
	return res.StatusCode, nil
}

// apiError reads the message of an error answer in either provider's
// shape: Cloudflare {errors: [{code, message}]}, ArvanCloud {message,
// errors: {field: [messages]}}.
func apiError(status int, b []byte) error {
	var e struct {
		Message string          `json:"message"`
		Errors  json.RawMessage `json:"errors"`
	}
	var msgs []string
	if json.Unmarshal(b, &e) == nil {
		if e.Message != "" {
			msgs = append(msgs, e.Message)
		}
		var list []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		var fields map[string][]string
		switch {
		case json.Unmarshal(e.Errors, &list) == nil:
			for _, x := range list {
				if x.Code != 0 {
					msgs = append(msgs, fmt.Sprintf("%s (code %d)", x.Message, x.Code))
				} else if x.Message != "" {
					msgs = append(msgs, x.Message)
				}
			}
		case json.Unmarshal(e.Errors, &fields) == nil:
			for f, ms := range fields {
				msgs = append(msgs, f+": "+strings.Join(ms, "; "))
			}
		}
	}
	if len(msgs) == 0 {
		s := strings.TrimSpace(string(b))
		if len(s) > 300 {
			s = s[:300] + "…"
		}
		if s != "" {
			msgs = append(msgs, s)
		}
	}
	hint := ""
	switch status {
	case 401, 403:
		hint = ": the token was rejected or lacks permissions"
	case 429:
		hint = ": rate limited, try again later"
	}
	if len(msgs) == 0 {
		return fmt.Errorf("HTTP %d%s", status, hint)
	}
	return fmt.Errorf("HTTP %d%s: %s", status, hint, strings.Join(msgs, "; "))
}
