// Package store is a small key-value settings store in a SQLite file shared by every process.
// Open creates ~/.loop/loop.db (or under $LOOP_HOME) with a single settings table,
// readable only by the user. Get, Set and Delete work directly; Update runs a function inside a
// BEGIN IMMEDIATE transaction so other processes wait for it, which auth relies on for token
// refresh. List returns every entry whose key starts with a prefix. Holds sign-in state for auth,
// the chosen model for tui, and saved conversations for session.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store is a small key-value store backed by a SQLite file shared by every process on the machine.
type Store struct {
	db *sql.DB
}

// Tx reads and writes settings inside a single write-locked transaction.
type Tx struct {
	conn *sql.Conn
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DefaultPath returns $LOOP_HOME/loop.db, or ~/.loop/loop.db when it is unset.
func DefaultPath() (string, error) {
	if home := os.Getenv("LOOP_HOME"); home != "" {
		return filepath.Join(home, "loop.db"), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".loop", "loop.db"), nil
}

// Open opens or creates the database at path, readable only by the current user.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Waits longer than the slowest token refresh, which holds the write lock across a network call.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(60000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create settings table: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Get returns the value for key and whether it was set.
func (s *Store) Get(ctx context.Context, key string) (string, bool, error) {
	return get(ctx, s.db, key)
}

func (s *Store) Set(ctx context.Context, key, value string) error {
	return set(ctx, s.db, key, value)
}

func (s *Store) Delete(ctx context.Context, key string) error {
	return remove(ctx, s.db, key)
}

// Entry is one key and its value.
type Entry struct {
	Key   string
	Value string
}

// List returns every entry whose key starts with prefix, in key order. The prefix must not be empty.
func (s *Store) List(ctx context.Context, prefix string) ([]Entry, error) {
	// A range on the primary key, so "_" and "%" in the prefix need no escaping.
	end := prefix[:len(prefix)-1] + string(prefix[len(prefix)-1]+1)
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings WHERE key >= ? AND key < ? ORDER BY key`, prefix, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Key, &e.Value); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// Update runs fn while holding the database write lock, so other processes wait until it commits.
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	err = fn(&Tx{conn: conn})
	if err == nil {
		// Commit even if ctx was cancelled meanwhile, so finished work is never thrown away.
		_, err = conn.ExecContext(context.WithoutCancel(ctx), "COMMIT")
	}
	if err != nil {
		_, rollbackErr := conn.ExecContext(context.Background(), "ROLLBACK")
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (tx *Tx) Get(ctx context.Context, key string) (string, bool, error) {
	return get(ctx, tx.conn, key)
}

func (tx *Tx) Set(ctx context.Context, key, value string) error {
	return set(ctx, tx.conn, key, value)
}

func (tx *Tx) Delete(ctx context.Context, key string) error {
	return remove(ctx, tx.conn, key)
}

func get(ctx context.Context, q querier, key string) (string, bool, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func set(ctx context.Context, q querier, key, value string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

func remove(ctx context.Context, q querier, key string) error {
	_, err := q.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}
