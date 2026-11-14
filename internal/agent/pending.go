package agent

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"opsdeck/internal/llm"
	"opsdeck/internal/workspace"
)

// Messages the user sends while a turn runs. A queued message waits for the
// turn to end and starts the next one (several queued messages go together,
// in order). A steering message joins the running turn at the next step
// boundary: after the tool calls of the current step have finished (with
// their approvals), before the next model request. A steering message that
// finds no next step (the turn ended) is sent like a queued one.
//
// Neither is a way around anything: the text is redacted like any message,
// a steering message answers no approval and does not lift the extra care
// after web content, and what the agent does next asks as usual.

// Message delivery modes.
const (
	DeliverQueue = "queue"
	DeliverSteer = "steer"
)

// PendingMessage is a message waiting to be delivered, as the chat shows it.
type PendingMessage struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"` // as typed (redacted)
	Mode        string       `json:"mode"` // queue | steer
	Created     time.Time    `json:"created"`
	Attachments []Attachment `json:"attachments,omitempty"`

	in turnInput
}

// ErrNoSuchPending is returned for a pending message that was delivered
// (or cancelled) already.
var ErrNoSuchPending = errors.New("this message was already sent or cancelled")

// Pending returns the messages waiting to be delivered.
func (s *Session) Pending() []PendingMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PendingMessage{}, s.queued...)
}

// emitPending tells the chat what waits (not stored: the session's GET has it).
func (s *Session) emitPending(returned []PendingMessage) {
	ev := map[string]any{"messages": s.Pending()}
	if len(returned) > 0 {
		ev["returned"] = returned
	}
	s.emit("pending", ev, false)
}

// enqueue keeps a message for the running turn; false when no turn runs
// (then it starts one as usual).
func (s *Session) enqueue(mode string, in turnInput, display string) (PendingMessage, bool) {
	p := PendingMessage{ID: "p-" + uuid.NewString()[:8], Text: display, Mode: mode, Created: time.Now().UTC(), Attachments: in.Files, in: in}
	s.mu.Lock()
	if s.cancel == nil {
		s.mu.Unlock()
		return p, false
	}
	s.queued = append(s.queued, p)
	s.mu.Unlock()
	s.emitPending(nil)
	return p, true
}

// CancelPending drops a message that waits.
func (m *Manager) CancelPending(ws, id, pendingID string) error {
	s, err := m.Get(ws, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	found := false
	for i, p := range s.queued {
		if p.ID == pendingID {
			s.queued = append(s.queued[:i:i], s.queued[i+1:]...)
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return ErrNoSuchPending
	}
	s.emitPending(nil)
	return nil
}

// takeSteering removes the steering messages that wait, in order.
func (s *Session) takeSteering() []PendingMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var steer, keep []PendingMessage
	for _, p := range s.queued {
		if p.Mode == DeliverSteer {
			steer = append(steer, p)
		} else {
			keep = append(keep, p)
		}
	}
	s.queued = keep
	return steer
}

// steer adds the steering messages that wait to the conversation at a step
// boundary: as text after the last user message (the tool results), so the
// model reads them with the results before its next step.
func (m *Manager) steer(s *Session) {
	msgs := s.takeSteering()
	if len(msgs) == 0 {
		return
	}
	s.mu.Lock()
	for _, p := range msgs {
		blocks := []llm.Block{{Type: "text", Text: "[The user sent this while you were working; take it into account from now on]\n" + p.in.Text}}
		blocks = append(blocks, p.in.Blocks...)
		if n := len(s.messages); n > 0 && s.messages[n-1].Role == "user" {
			s.messages[n-1].Content = append(s.messages[n-1].Content, blocks...)
		} else {
			s.messages = append(s.messages, llm.Message{Role: "user", Content: blocks})
		}
	}
	s.mu.Unlock()
	for _, p := range msgs {
		ev := map[string]any{"text": p.Text, "pendingId": p.ID, "steered": true}
		if len(p.Attachments) > 0 {
			ev["attachments"] = p.Attachments
		}
		if p.in.Command != "" {
			ev["command"], ev["prompt"] = p.in.Command, p.in.Text
		}
		s.emit("user", ev, true)
	}
	s.emitPending(nil)
	s.save()
}

// takeQueued removes every message that waits (queued, and steering ones
// that found no step boundary), in the order they were sent.
func (s *Session) takeQueued() []PendingMessage {
	q := s.queued
	s.queued = nil
	return q
}

// deliverQueued starts the next turn with the messages that waited for the
// last one: one turn, each message shown as sent, the model getting them in
// order. Put back when another turn started first.
func (m *Manager) deliverQueued(s *Session, w *workspace.Workspace, q []PendingMessage) bool {
	if len(q) == 0 {
		return false
	}
	in := turnInput{}
	var texts []string
	for _, p := range q {
		texts = append(texts, p.in.Text)
		in.Blocks = append(in.Blocks, p.in.Blocks...)
		in.Files = append(in.Files, p.in.Files...)
	}
	in.Text = strings.Join(texts, "\n\n")
	in.Delivered = q
	if err := m.start(s, w, in); err != nil {
		s.mu.Lock()
		s.queued = append(q, s.queued...)
		s.mu.Unlock()
		s.emitPending(nil)
		return false
	}
	return true
}
