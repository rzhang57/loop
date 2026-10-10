package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "loop.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestSetGetDelete(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	if _, ok, err := s.Get(ctx, "model"); err != nil || ok {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := s.Set(ctx, "model", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "model", "b"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.Get(ctx, "model"); err != nil || !ok || v != "b" {
		t.Fatalf("got %q ok=%v err=%v, want b", v, ok, err)
	}
	if err := s.Delete(ctx, "model"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "model"); ok {
		t.Fatal("key still present after delete")
	}
}

func TestFilesArePrivate(t *testing.T) {
	_, path := openTemp(t)
	for _, p := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s has mode %v, want no group or other access", p, perm)
		}
	}
}

func TestUpdateRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	boom := errors.New("boom")

	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.Set(ctx, "k", "v"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
	if _, ok, _ := s.Get(ctx, "k"); ok {
		t.Fatal("write survived a failed update")
	}
}

func TestUpdateSerializesAcrossConnections(t *testing.T) {
	ctx := context.Background()
	first, path := openTemp(t)
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	const increments = 20
	var wg sync.WaitGroup
	for _, s := range []*Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range increments {
				err := s.Update(ctx, func(tx *Tx) error {
					v, _, err := tx.Get(ctx, "counter")
					if err != nil {
						return err
					}
					n, _ := strconv.Atoi(v)
					return tx.Set(ctx, "counter", strconv.Itoa(n+1))
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	v, _, _ := first.Get(ctx, "counter")
	if v != strconv.Itoa(2*increments) {
		t.Fatalf("counter = %s, want %d: concurrent updates were lost", v, 2*increments)
	}
}

func TestUpdateCommitsWhenCancelledAfterWork(t *testing.T) {
	s, _ := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())

	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.Set(ctx, "k", "v"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if err != nil {
		t.Fatalf("finished work should commit: %v", err)
	}
	if v, _, _ := s.Get(context.Background(), "k"); v != "v" {
		t.Fatal("write was lost")
	}
	if err := s.Update(context.Background(), func(tx *Tx) error { return tx.Set(context.Background(), "k", "w") }); err != nil {
		t.Fatalf("the write lock was left held: %v", err)
	}
}

func TestDefaultPathHonoursLoopHome(t *testing.T) {
	t.Setenv("LOOP_HOME", "/tmp/somewhere")
	if p, _ := DefaultPath(); p != "/tmp/somewhere/loop.db" {
		t.Fatalf("got %s", p)
	}
	t.Setenv("LOOP_HOME", "")
	home, _ := os.UserHomeDir()
	if p, _ := DefaultPath(); p != filepath.Join(home, ".loop", "loop.db") {
		t.Fatalf("got %s", p)
	}
}

func TestListReturnsOnlyKeysWithThePrefix(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, key := range []string{"session.b", "session.a", "sessionx", "session", "sessio", "model", "session/"} {
		if err := s.Set(ctx, key, "v-"+key); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := s.List(ctx, "session.")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{{"session.a", "v-session.a"}, {"session.b", "v-session.b"}}
	if len(entries) != len(want) || entries[0] != want[0] || entries[1] != want[1] {
		t.Fatalf("got %v, want %v", entries, want)
	}
	if entries, err := s.List(ctx, "nothing."); err != nil || len(entries) != 0 {
		t.Fatalf("got %v err=%v, want none", entries, err)
	}
}
