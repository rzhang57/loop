package session

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rzhang57/loop/internal/llm"
	"github.com/rzhang57/loop/internal/store"
)

func TestSaveAndLoadKeepEveryField(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	want := Session{ID: "abc", Model: "gpt-5.6-luna", Dir: "/work", Updated: time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC), Items: []llm.Item{
		llm.UserMessage("hi"),
		{Type: "function_call", CallID: "c1", Name: "bash", Namespace: "local", Arguments: `{"command":"ls"}`},
		llm.ToolOutput("c1", "README.md"),
		{Type: "function_call_output", CallID: "c2", Output: "half\nerror: exit status 1", Failed: true},
		llm.AssistantMessage("done"),
	}}
	if err := Save(ctx, s, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(ctx, s, "abc")
	if err != nil || !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := Load(ctx, s, "missing"); ok {
		t.Fatal("missing session reported as found")
	}
}

func TestNewIDSortsByTime(t *testing.T) {
	early := NewID(time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC))
	late := NewID(time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC))
	if !(early < late) || !strings.HasPrefix(early, "20261004-090000-") {
		t.Fatalf("%s should sort before %s", early, late)
	}
}

func TestCorruptSessionIsAnError(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Set(context.Background(), "session.bad", "{not json")
	if _, ok, err := Load(context.Background(), s, "bad"); err == nil || ok {
		t.Fatalf("got ok=%v err=%v, want an error", ok, err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestListShowsNewestFirstWithTitles(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	at := func(hour int) time.Time { return time.Date(2026, 10, 8, hour, 0, 0, 0, time.UTC) }
	for _, sess := range []Session{
		{ID: "20261008-080000-aaaaaa", Model: "m1", Dir: "/a", Updated: at(8), Items: []llm.Item{llm.UserMessage("  first task\nmore detail"), llm.AssistantMessage("ok")}},
		{ID: "20261008-070000-bbbbbb", Model: "m2", Dir: "/b", Updated: at(10), Items: []llm.Item{llm.UserMessage("second task")}},
	} {
		if err := Save(ctx, s, sess); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []Summary{
		{ID: "20261008-070000-bbbbbb", Title: "second task", Model: "m2", Dir: "/b", Updated: at(10)},
		{ID: "20261008-080000-aaaaaa", Title: "first task", Model: "m1", Dir: "/a", Updated: at(8)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestListReadsOldRecordsAndSkipsBadOnes(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	s.Set(ctx, "session.20261004-191709-a0d7d5", `{"model":"m","items":[{"type":"function_call_output","call_id":"c","output":"x"},{"type":"message","role":"user","content":"old task"}]}`)
	s.Set(ctx, "session.20261005-090000-cccccc", "{not json")
	s.Set(ctx, "sessionx", `{"model":"other","items":[]}`)
	s.Set(ctx, "model", "m")

	got, err := List(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 4, 19, 17, 9, 0, time.Local)
	if len(got) != 1 || got[0].ID != "20261004-191709-a0d7d5" || got[0].Title != "old task" || got[0].Model != "m" || !got[0].Updated.Equal(created) {
		t.Fatalf("got %+v", got)
	}
}
