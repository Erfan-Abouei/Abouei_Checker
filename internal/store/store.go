// Package store owns SQLite persistence for AboueiChecker: connection
// lifecycle, pragmas, versioned migrations, and repositories for backend
// state, observations, incidents, and the notification outbox.
//
// All state transitions for one probe are committed in a single transaction.
// Network I/O never happens inside a transaction. Callers must treat returned
// values as snapshots; the database is the source of truth.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Register the pure-Go SQLite driver for database/sql. This is the only
	// blank import in non-test code: it wires the approved storage engine
	// without exposing the driver package to the rest of the program.
	_ "modernc.org/sqlite"
)

const (
	// SupportedSchemaVersion is the only user_version this binary opens
	// read-write. Newer databases fail closed with upgrade guidance.
	SupportedSchemaVersion = 1

	busyTimeoutMs = 5000
)

// OpenError classifies failures to initialize the database so the CLI can map
// them to exit code 3 with a safe message.
type OpenError struct {
	// Reason is a stable machine-readable cause: "path", "permissions",
	// "schema_newer", "schema_inconsistent", "corrupt", "migration", or
	// "internal".
	Reason string
	// Path is the database path as configured (never includes secrets; the
	// path itself is operator configuration, not a secret).
	Path string
	// Message is the safe user-facing summary.
	Message string

	cause error
}

// Error implements error using the safe message.
func (e *OpenError) Error() string {
	if e == nil {
		return "database unavailable"
	}
	return e.Message
}

// Unwrap exposes the internal cause for diagnostics.
func (e *OpenError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Store is an open SQLite database with migration 1 applied. The zero value
// is not usable; construct with Open. A Store allows one writer at a time
// (MaxOpenConns(1)) and must be closed with Close.
type Store struct {
	db   *sql.DB
	path string
}

// Path returns the database file path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Open creates parent directories, opens the database file, applies the
// connection policy, runs forward migrations, and resets stale delivery
// claims. It never deletes, recreates, or repairs an existing database.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, &OpenError{Reason: "path", Path: path, Message: "database path is not configured"}
	}
	clean := filepath.Clean(path)
	if dir := filepath.Dir(clean); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, &OpenError{
				Reason:  "permissions",
				Path:    clean,
				Message: fmt.Sprintf("cannot create database directory %s", dir),
				cause:   err,
			}
		}
	}

	created, statErr := fileExists(clean)
	if statErr != nil {
		return nil, &OpenError{
			Reason:  "permissions",
			Path:    clean,
			Message: fmt.Sprintf("cannot access database path %s", clean),
			cause:   statErr,
		}
	}
	if !created {
		if infoErr := checkParentWritable(clean); infoErr != nil {
			return nil, infoErr
		}
	} else if err := ensureRegularFile(clean); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", "file:"+clean+"?_pragma=busy_timeout%3D5000")
	if err != nil {
		return nil, &OpenError{Reason: "internal", Path: clean, Message: "cannot open monitoring database", cause: err}
	}
	store := &Store{db: db, path: clean}
	// One writer connection serializes transactions and avoids SQLITE_BUSY
	// complexity under the single-process assumption.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := store.applyPragmas(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.chmodIfCreated(created); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.resetStaleDelivering(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying handle for repositories and tests within this
// package boundary. It must not be used to run network I/O inside a
// transaction.
func (s *Store) DB() *sql.DB {
	return s.db
}

func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	_ = info
	return true, nil
}

func ensureRegularFile(path string) *OpenError {
	info, err := os.Stat(path)
	if err != nil {
		return &OpenError{Reason: "permissions", Path: path, Message: fmt.Sprintf("cannot access database path %s", path), cause: err}
	}
	if !info.Mode().IsRegular() {
		return &OpenError{
			Reason:  "path",
			Path:    path,
			Message: fmt.Sprintf("database path %s is not a regular file", path),
		}
	}
	return nil
}

func checkParentWritable(path string) *OpenError {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Open creates it.
		}
		return &OpenError{Reason: "permissions", Path: path, Message: fmt.Sprintf("cannot access database directory %s", dir), cause: err}
	}
	if !info.IsDir() {
		return &OpenError{Reason: "path", Path: path, Message: fmt.Sprintf("database parent %s is not a directory", dir)}
	}
	return nil
}

func (s *Store) applyPragmas() error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA temp_store=MEMORY",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMs),
	}
	for _, pragma := range pragmas {
		if _, err := s.db.Exec(pragma); err != nil {
			return &OpenError{
				Reason:  "corrupt",
				Path:    s.path,
				Message: "monitoring database is not usable",
				cause:   fmt.Errorf("%s: %w", pragma, err),
			}
		}
	}
	return nil
}

// chmodIfCreated restricts a newly created database file to owner-only access.
// created reports whether the file already existed before Open ran.
func (s *Store) chmodIfCreated(existed bool) error {
	if existed {
		return nil
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return &OpenError{
			Reason:  "permissions",
			Path:    s.path,
			Message: "cannot protect monitoring database file",
			cause:   err,
		}
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_ = os.Chmod(s.path+suffix, 0o600)
	}
	return nil
}

// nowMillis returns UTC Unix milliseconds for storage.
func nowMillis(t time.Time) int64 {
	return t.UTC().UnixMilli()
}

// millisToTime converts stored UTC milliseconds back to a time.
func millisToTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}
