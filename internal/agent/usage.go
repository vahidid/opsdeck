package agent

import (
	"context"
	"time"

	"opsdeck/internal/llm"
)

// ContextUse is how much of the model's context window the conversation
// took at its latest model request.
type ContextUse struct {
	Model string `json:"model"`          // "providerId/model" the numbers are for
	Used  int    `json:"used,omitempty"` // tokens of the latest request and its response; 0 = not reported yet
	// Window is the model's context window as its provider reports it;
	// 0 = the provider does not report it (it is never guessed).
	Window int `json:"window,omitempty"`
}

// contextUsed is what a model response says the conversation takes now:
// everything the model read (cached or not) plus what it wrote, which goes
// into the next request. 0 when the provider reported no usage.
func contextUsed(u llm.Usage) int {
	return u.InputTokens + u.OutputTokens
}

// addUsage adds a response's tokens to the session's totals and, for the
// session's own conversation (not a subagent's), records its context; then
// it tells the chat. Called without s.mu held.
func (s *Session) addUsage(model string, u llm.Usage, own bool) {
	s.mu.Lock()
	s.info.Usage.InputTokens += u.InputTokens
	s.info.Usage.OutputTokens += u.OutputTokens
	s.info.Usage.CacheRead += u.CacheRead
	if used := contextUsed(u); own && used > 0 {
		c := ContextUse{Model: model, Used: used}
		if s.info.Context != nil && s.info.Context.Model == model {
			c.Window = s.info.Context.Window
		}
		s.info.Context = &c
	}
	s.mu.Unlock()
	s.emitUsage()
}

// setWindow records the context window of the session's model.
func (s *Session) setWindow(model string, window int) {
	s.mu.Lock()
	c := ContextUse{Model: model}
	if s.info.Context != nil && s.info.Context.Model == model {
		c = *s.info.Context
	}
	changed := c.Window != window
	c.Window = window
	s.info.Context = &c
	s.mu.Unlock()
	if changed {
		s.emitUsage()
	}
}

// emitUsage sends the session's token totals and context to the chat (not
// stored: the session info has them).
func (s *Session) emitUsage() {
	s.mu.Lock()
	ev := map[string]any{"usage": s.info.Usage}
	if s.info.Context != nil {
		c := *s.info.Context
		ev["context"] = c
	}
	s.mu.Unlock()
	s.emit("usage", ev, false)
}

// fillWindow asks the provider for the model's context window in the
// background, so the turn does not wait for it.
func (m *Manager) fillWindow(s *Session, modelID string) {
	if m.deps.Router == nil {
		return
	}
	router := m.deps.Router()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		n, err := router.ContextWindow(ctx, modelID)
		if err != nil {
			return // not reported; the chat says so
		}
		s.setWindow(modelID, n)
	}()
}
