package agent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"opsdeck/internal/llm"
)

// Attachments are files the user adds to a message: screenshots, logs,
// configs, PDFs. They are kept next to the session (<session>.files/), not
// in its transcript; images and PDFs go to the model as such (read from
// disk for each request), text files as text, through the redactor.

// Attachment describes one file.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	Kind string `json:"kind"` // image | pdf | text | file
}

const (
	// MaxAttachment is the largest file the user can attach.
	MaxAttachment = 20 << 20
	// maxImage is the largest image a model API takes.
	maxImage = 5 << 20
	// maxAttachmentText is how much of a text file goes to the model.
	maxAttachmentText = 200_000
	// maxAttachments per message.
	maxAttachments = 10
)

var attachIDRe = regexp.MustCompile(`^a-[a-f0-9]{10}$`)

func (s *Session) filesDir() string { return strings.TrimSuffix(s.path, ".json") + ".files" }

// AddAttachment stores a file for the session's next message.
func (m *Manager) AddAttachment(ws, id, name string, r io.Reader) (Attachment, error) {
	s, err := m.Get(ws, id)
	if err != nil {
		return Attachment{}, err
	}
	if s.Info().Client != "" {
		return Attachment{}, ErrClientSession
	}
	name = cleanAttachmentName(name)
	dir := s.filesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, err
	}
	a := Attachment{ID: "a-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10], Name: name}
	path := filepath.Join(dir, a.ID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Attachment{}, err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxAttachment+1))
	f.Close()
	if err == nil && n > MaxAttachment {
		err = fmt.Errorf("%s is larger than %d MB", name, MaxAttachment>>20)
	}
	if err == nil && n == 0 {
		err = fmt.Errorf("%s is empty", name)
	}
	if err != nil {
		os.Remove(path)
		return Attachment{}, err
	}
	a.Size = n
	a.Mime, a.Kind = sniff(path, name)
	if a.Kind == "image" && n > maxImage {
		os.Remove(path)
		return Attachment{}, fmt.Errorf("%s: images can be up to %d MB", name, maxImage>>20)
	}
	b, _ := json.Marshal(a)
	if err := os.WriteFile(path+".json", b, 0o600); err != nil {
		os.Remove(path)
		return Attachment{}, err
	}
	return a, nil
}

// Attachment returns a stored file's description and path.
func (s *Session) Attachment(aid string) (Attachment, string, error) {
	if !attachIDRe.MatchString(aid) {
		return Attachment{}, "", fmt.Errorf("invalid attachment id %q", aid)
	}
	path := filepath.Join(s.filesDir(), aid)
	b, err := os.ReadFile(path + ".json")
	if err != nil {
		return Attachment{}, "", fmt.Errorf("attachment %s not found", aid)
	}
	var a Attachment
	if err := json.Unmarshal(b, &a); err != nil {
		return Attachment{}, "", err
	}
	return a, path, nil
}

func cleanAttachmentName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == "/" {
		name = "file"
	}
	if r := []rune(name); len(r) > 120 {
		name = string(r[:120])
	}
	return name
}

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// sniff tells the type of a file from its first bytes (and its name).
func sniff(path, name string) (mime, kind string) {
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream", "file"
	}
	defer f.Close()
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	mime = http.DetectContentType(head)
	if i := strings.Index(mime, ";"); i > 0 {
		mime = mime[:i]
	}
	switch {
	case imageTypes[mime]:
		return mime, "image"
	case mime == "application/pdf":
		return mime, "pdf"
	}
	if !strings.ContainsRune(string(head), 0) && validUTF8Prefix(head) {
		if strings.HasPrefix(mime, "text/") {
			return mime, "text"
		}
		return "text/plain", "text"
	}
	return mime, "file"
}

// validUTF8Prefix allows a rune cut at the end of the sample.
func validUTF8Prefix(b []byte) bool {
	for i := 0; i < 4 && len(b) > 0; i++ {
		if utf8.Valid(b) {
			return true
		}
		b = b[:len(b)-1]
	}
	return utf8.Valid(b)
}

// attachmentBlocks turns a message's attachments into content for the
// model: image and PDF blocks (bytes filled in per request) and text.
func (m *Manager) attachmentBlocks(s *Session, ids []string) ([]llm.Block, []Attachment, error) {
	if len(ids) > maxAttachments {
		return nil, nil, fmt.Errorf("at most %d attachments per message", maxAttachments)
	}
	var blocks []llm.Block
	var list []Attachment
	for _, id := range ids {
		a, path, err := s.Attachment(id)
		if err != nil {
			return nil, nil, err
		}
		list = append(list, a)
		switch a.Kind {
		case "image":
			blocks = append(blocks, llm.Block{Type: "image", MediaType: a.Mime, Name: a.Name, Source: a.ID})
		case "pdf":
			blocks = append(blocks, llm.Block{Type: "document", MediaType: a.Mime, Name: a.Name, Source: a.ID})
		case "text":
			b, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, err
			}
			text := string(b)
			cut := ""
			if len(text) > maxAttachmentText {
				text = strings.ToValidUTF8(text[:maxAttachmentText], "")
				cut = fmt.Sprintf("\n[… cut: the file has %d bytes; only the first %d are shown]", len(b), maxAttachmentText)
			}
			blocks = append(blocks, llm.Block{Type: "text", Text: fmt.Sprintf("<attachment name=%q>\n%s%s\n</attachment>", a.Name, m.redactor.Redact(text), cut)})
		default:
			blocks = append(blocks, llm.Block{Type: "text", Text: fmt.Sprintf("[The user attached %s (%s, %d bytes); its content cannot be shown to you.]", a.Name, a.Mime, a.Size)})
		}
	}
	return blocks, list, nil
}

// withAttachmentData returns the history with the bytes of image and PDF
// blocks filled in, for one request. The session's own messages are not
// changed. A file that is gone is sent as a placeholder.
func (s *Session) withAttachmentData(history []llm.Message) []llm.Message {
	out := history
	copied := false
	for i, msg := range history {
		for j, b := range msg.Content {
			if (b.Type != "image" && b.Type != "document") || b.Source == "" || b.Data != "" {
				continue
			}
			if !copied {
				out = append([]llm.Message(nil), history...)
				copied = true
			}
			if &out[i].Content[0] == &history[i].Content[0] {
				out[i].Content = append([]llm.Block(nil), msg.Content...)
			}
			if _, path, err := s.Attachment(b.Source); err == nil {
				if data, err := os.ReadFile(path); err == nil {
					out[i].Content[j].Data = base64.StdEncoding.EncodeToString(data)
				}
			}
		}
	}
	return out
}

// errNoMessage is returned for a message without text or files.
var errNoMessage = errors.New("empty message")
