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
	"errors"
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

	// openMaxAttempts and openRetryDelay bound retries when several
	// processes initialize the same database file concurrently (for
	// example, overlapping one-shot cron runs racing creation, WAL setup,
	// or migration). Only lock-contention errors retry; permissions,
	// schema, and corruption failures return immediately.
	openMaxAttempts = 25
	openRetryDelay  = 200 * time.Millisecond
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

	openDB := func() (*sql.DB, error) {
		db, err := sql.Open("sqlite", "file:"+clean+"?_pragma=busy_timeout%3D5000")
		if err != nil {
			return nil, &OpenError{Reason: "internal", Path: clean, Message: "cannot open monitoring database", cause: err}
		}
		return db, nil
	}
	// Concurrent creators race WAL setup, migration DDL, and the stale
	// delivery reset. Retry only transient lock contention with a fresh
	// handle per attempt; permanent failures fail closed immediately.
	for attempt := 1; ; attempt++ {
		db, err := openDB()
		if err != nil {
			return nil, err
		}
		store, initErr := initialize(clean, created, db)
		if initErr == nil {
			return store, nil
		}
		_ = db.Close()
		if !isOpenRetryable(initErr) || attempt >= openMaxAttempts {
			return nil, initErr
		}
		time.Sleep(openRetryDelay)
	}
}

// initialize applies pragmas, migrations, file protection, and the stale
// delivery reset on an already-opened handle. It never closes the handle;
// the caller owns it and closes it when initialization ultimately fails.
// existed reports whether the database file already existed before this
// process opened it; a concurrent creator may have created it since.
func initialize(clean string, existed bool, db *sql.DB) (*Store, error) {
	store := &Store{db: db, path: clean}
	// One writer connection serializes transactions and avoids SQLITE_BUSY
	// complexity under the single-process assumption.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := store.applyPragmas(); err != nil {
		return nil, err
	}
	if err := store.migrate(); err != nil {
		return nil, err
	}
	if err := store.chmodIfCreated(existed); err != nil {
		return nil, err
	}
	if err := store.resetStaleDelivering(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

// IsTransient reports whether err reflects transient cross-process SQLite
// contention (lock waits or concurrent creation races) that a bounded retry
// may resolve. Permanent failures (permissions, schema, corruption without
// a contention signature) return false so callers still fail closed.
func IsTransient(err error) bool {
	return isLockContention(err)
}

// withTransientRetry executes fn until it succeeds, the context ends, the
// error is permanent, or the attempt budget is exhausted. It exists for
// overlapping writers (cron one-shots racing each other or a monitor):
// SQLite serializes writers, and the loser observes a transient busy error
// that a short retry resolves. Every retried operation is idempotent or
// fully rolled back, so retries never duplicate incidents or events.
func withTransientRetry(ctx context.Context, fn func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if !isLockContention(err) || attempt >= openMaxAttempts {
			return err
		}
		timer := time.NewTimer(openRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// isOpenRetryable reports whether an initialization failure may resolve by
// reopening after a short delay. "corrupt" and "migration" reasons can wrap
// transient cross-process contention (creation races, WAL setup, concurrent
// DDL), and "schema_inconsistent" can be a torn read of a concurrent
// creator's commit landing between the version and table checks. Every
// other reason (path, permissions, schema_newer, internal) is permanent and
// must fail closed on the first attempt. The budget stays bounded, so a
// genuinely broken database still fails closed — a few seconds later.
func isOpenRetryable(err error) bool {
	var openErr *OpenError
	if !errors.As(err, &openErr) {
		return isLockContention(err)
	}
	switch openErr.Reason {
	case "corrupt", "migration", "schema_inconsistent":
	default:
		return false
	}
	if cause := openErr.Unwrap(); cause != nil {
		message := cause.Error()
		if isBusyMessage(message) || isConcurrentCreation(message) {
			return true
		}
	}
	return false
}

// isLockContention reports whether any level of err's chain carries a
// transient SQLite lock signature. It walks the full unwrap chain so both
// driver errors and wrapped application errors are recognized. Only lock
// waits retry at runtime; concurrent-creation conflicts are an
// initialization-only concern handled by isOpenRetryable.
func isLockContention(err error) bool {
	for err != nil {
		if isBusyMessage(err.Error()) {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// isConcurrentCreation matches the DDL conflict when two processes
// initialize the same fresh database file at once: both observe version 0
// and both attempt the same CREATE TABLE. The loser's transaction rolls
// back; retrying lets it observe the winner's committed schema. The retry
// budget stays bounded, and a genuinely inconsistent schema still fails
// closed once the winner's version is absent.
func isConcurrentCreation(message string) bool {
	return strings.Contains(strings.ToLower(message), "already exists")
}

// isBusyMessage matches the SQLite SQLITE_BUSY family without importing
// driver internals: the busy timeout already covers short intra-process
// waits, so anything surfacing here is cross-process contention.
func isBusyMessage(message string) bool {
	lowered := strings.ToLower(message)
	for _, fragment := range []string{
		"database is locked",
		"database table is locked",
		"database schema is locked",
		"sqlite_busy",
	} {
		if strings.Contains(lowered, fragment) {
			return true
		}
	}
	return false
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
