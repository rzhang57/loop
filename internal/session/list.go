// Listing saved sessions for a picker.
// List reads every "session.<id>" record from the store and returns a Summary of each, newest
// first. Records saved before titles, directories and save times were kept fall back to the first
// user message and the time in the ID. Records that cannot be read are skipped so one bad row does
// not hide the rest.

package session

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/store"
)

// Summary describes a saved session without its history.
type Summary struct {
	ID      string
	Title   string
	Model   string
	Dir     string
	Updated time.Time
}

// summaryRecord is the part of a record a Summary needs; tool output is skipped.
type summaryRecord struct {
	Model   string    `json:"model"`
	Dir     string    `json:"dir"`
	Updated time.Time `json:"updated"`
	Title   string    `json:"title"`
	Items   []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"items"`
}

// List returns every saved session, most recently saved first.
func List(ctx context.Context, s *store.Store) ([]Summary, error) {
	entries, err := s.List(ctx, key(""))
	if err != nil {
		return nil, err
	}
	var summaries []Summary
	for _, e := range entries {
		var rec summaryRecord
		if json.Unmarshal([]byte(e.Value), &rec) != nil {
			continue
		}
		id := strings.TrimPrefix(e.Key, key(""))
		sum := Summary{ID: id, Title: rec.Title, Model: rec.Model, Dir: rec.Dir, Updated: rec.Updated}
		if sum.Title == "" {
			var items []llm.Item
			for _, item := range rec.Items {
				items = append(items, llm.Item{Type: item.Type, Role: item.Role, Content: item.Content})
			}
			sum.Title = title(items)
		}
		if sum.Updated.IsZero() {
			sum.Updated = createdAt(id)
		}
		summaries = append(summaries, sum)
	}
	slices.SortStableFunc(summaries, func(a, b Summary) int {
		return cmp.Or(b.Updated.Compare(a.Updated), strings.Compare(b.ID, a.ID))
	})
	return summaries, nil
}

// createdAt reads the creation time NewID put at the start of id, or the zero time if it has none.
func createdAt(id string) time.Time {
	if len(id) < len(idLayout) {
		return time.Time{}
	}
	t, _ := time.ParseInLocation(idLayout, id[:len(idLayout)], time.Local)
	return t
}
