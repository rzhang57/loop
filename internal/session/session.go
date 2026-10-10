// Package session saves and loads conversations so a later run can continue one.
// A Session is an ID, a model slug, the working directory, when it was last saved and the llm.Item
// history. Save converts the items to a JSON record, titled by the first thing the user asked, and
// stores it in store under "session.<id>"; Load reads it back. NewID makes IDs that sort by
// creation time, and list.go lists saved sessions newest first. Used by headless for `loop run --session` and by tui to resume.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/store"
)

// Session is a saved conversation that later runs can continue.
type Session struct {
	ID      string
	Model   string
	Dir     string
	Updated time.Time
	Items   []llm.Item
}

type record struct {
	Model   string       `json:"model"`
	Dir     string       `json:"dir,omitempty"`
	Updated time.Time    `json:"updated,omitzero"`
	Title   string       `json:"title,omitempty"`
	Items   []itemRecord `json:"items"`
}

type itemRecord struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
}

const idLayout = "20060102-150405"

// NewID returns a session ID that sorts by creation time.
func NewID(now time.Time) string {
	suffix := make([]byte, 3)
	rand.Read(suffix)
	return now.Format(idLayout) + "-" + hex.EncodeToString(suffix)
}

// Load returns the saved session, and false if there is none with that ID.
func Load(ctx context.Context, s *store.Store, id string) (Session, bool, error) {
	raw, ok, err := s.Get(ctx, key(id))
	if err != nil || !ok {
		return Session{}, ok, err
	}
	var rec record
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return Session{}, false, fmt.Errorf("read session %s: %w", id, err)
	}
	sess := Session{ID: id, Model: rec.Model, Dir: rec.Dir, Updated: rec.Updated}
	for _, r := range rec.Items {
		item := llm.Item{Type: r.Type, Role: r.Role, Content: r.Content, CallID: r.CallID, Name: r.Name,
			Namespace: r.Namespace, Arguments: r.Arguments, Output: r.Output, Failed: r.Failed}
		sess.Items = append(sess.Items, item)
	}
	return sess, true, nil
}

// Save writes the session, replacing any earlier copy.
func Save(ctx context.Context, s *store.Store, sess Session) error {
	rec := record{Model: sess.Model, Dir: sess.Dir, Updated: sess.Updated, Title: title(sess.Items)}
	for _, item := range sess.Items {
		r := itemRecord{Type: item.Type, Role: item.Role, Content: item.Content, CallID: item.CallID, Name: item.Name,
			Namespace: item.Namespace, Arguments: item.Arguments, Output: item.Output, Failed: item.Failed}
		rec.Items = append(rec.Items, r)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.Set(ctx, key(sess.ID), string(raw))
}

// title is the first line of the first user message, or "" if there is none.
func title(items []llm.Item) string {
	for _, item := range items {
		if item.Type == "message" && item.Role == "user" {
			line, _, _ := strings.Cut(strings.TrimSpace(item.Content), "\n")
			return line
		}
	}
	return ""
}

func key(id string) string {
	return "session." + id
}
