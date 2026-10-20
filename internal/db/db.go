package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/logx"
)

const (
	busyTimeout      = 30 * time.Second
	ShortLockWait    = 5 * time.Second
	lockPollInterval = 10 * time.Millisecond
	lockLogAfter     = ShortLockWait
)

var log = logx.Logger("atrium.db")

func connPragmas(foreignKeys bool) []string {
	fk := "PRAGMA foreign_keys = OFF"
	if foreignKeys {
		fk = "PRAGMA foreign_keys = ON"
	}
	return []string{busyPragma(busyTimeout), fk, "PRAGMA synchronous = FULL", "PRAGMA journal_size_limit = 67108864"}
}

func busyPragma(d time.Duration) string {
	return fmt.Sprintf("PRAGMA busy_timeout = %d", d.Milliseconds())
}

// Checkpoint is the result of PRAGMA wal_checkpoint: Busy is true when a reader or writer kept it from finishing,
// Frames is the WAL's length in frames and Done how many of them are in the database file.
type Checkpoint struct {
	Busy   bool
	Frames int64
	Done   int64
}

// Checkpoint runs PRAGMA wal_checkpoint(TRUNCATE) on the writer, waiting at most wait for readers, so a caller
// holding a lock is not kept for the full busy timeout.
func (d *DB) Checkpoint(ctx context.Context, wait time.Duration) (Checkpoint, error) {
	conn, err := d.Write.Conn(ctx)
	if err != nil {
		return Checkpoint{}, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, busyPragma(wait)); err != nil {
		return Checkpoint{}, err
	}
	var c Checkpoint
	var busy int64
	err = conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &c.Frames, &c.Done)
	c.Busy = busy != 0
	if _, rerr := conn.ExecContext(context.WithoutCancel(ctx), busyPragma(busyTimeout)); rerr != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return c, err
}

// DB is atrium.db: Write is the single writer connection, whose
// transactions begin IMMEDIATE; Read is the reader pool and cannot write.
type DB struct {
	Write *sql.DB
	Read  *sql.DB
}

// Connect opens the SQLite file at path, creating its directory, and sets
// WAL once.
func Connect(path string) (*DB, error) { return connect(path, true) }

// ConnectNoForeignKeys is Connect without foreign key enforcement, as
// Python's db.connect_memory; only tests that need that use it.
func ConnectNoForeignKeys(path string) (*DB, error) { return connect(path, false) }

func connect(path string, foreignKeys bool) (*DB, error) {
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("db: path %q contains '?'", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return nil, err
	}
	pragmas := connPragmas(foreignKeys)
	w := Open(path+"?_txlock=immediate", pragmas...)
	w.SetMaxOpenConns(1)
	if err := setWAL(w); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("db: %s: %w", path, err)
	}
	r := Open(path, append(pragmas, "PRAGMA query_only = ON")...)
	return &DB{Write: w, Read: r}, nil
}

func (d *DB) Close() error {
	return errors.Join(d.Read.Close(), d.Write.Close())
}

// SQLite skips the busy handler when switching a new file to WAL, so
// concurrent first connects get SQLITE_BUSY at once; retry for the
// busy_timeout instead.
func setWAL(w *sql.DB) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := w.Exec("PRAGMA journal_mode = WAL")
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WithTx runs fn in an IMMEDIATE transaction on the writer, committing when
// fn returns nil and rolling back on an error or panic.
func (d *DB) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Lock is an advisory flock held until Close or process exit.
type Lock struct{ f *os.File }

type LockTimeout struct {
	Path    string
	Timeout time.Duration
}

func (e *LockTimeout) Error() string {
	return fmt.Sprintf("db: timed out after %s waiting for exclusive lock on %s", e.Timeout, e.Path)
}

func Exclusive(ctx context.Context, path string, wait time.Duration) (*Lock, error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o777); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(lockPollInterval)
	defer ticker.Stop()
	started := time.Now()
	logged := false
	var timeout <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		timeout = timer.C
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		if wait == 0 && !logged && time.Since(started) >= lockLogAfter {
			log.InfoContext(ctx, "waiting for "+lock)
			logged = true
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			if err := ctx.Err(); err != nil {
				_ = f.Close()
				return nil, err
			}
			return &Lock{f: f}, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN):
			_ = f.Close()
			return nil, fmt.Errorf("db: flock %s: %w", lock, err)
		case wait > 0 && timeout == nil:
			_ = f.Close()
			return nil, &LockTimeout{Path: lock, Timeout: wait}
		default:
			select {
			case <-ctx.Done():
				_ = f.Close()
				return nil, ctx.Err()
			case <-timeout:
				timeout = nil
			case <-ticker.C:
			}
		}
	}
}

func (l *Lock) Close() error { return l.f.Close() }

// Open returns a pool on the SQLite file at path through the driver selected
// at build time (open_cgo.go or open_modernc.go). Each pragma statement runs
// on every new connection.
func Open(path string, pragmas ...string) *sql.DB {
	return sql.OpenDB(connector{drv: openDriver(), dsn: path, pragmas: pragmas})
}

type connector struct {
	drv     driver.Driver
	dsn     string
	pragmas []string
}

func (c connector) Driver() driver.Driver { return c.drv }

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	if len(c.pragmas) == 0 {
		return conn, nil
	}
	exec, ok := conn.(driver.ExecerContext)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("db: %T cannot execute pragmas", conn)
	}
	for _, p := range c.pragmas {
		if _, err := exec.ExecContext(ctx, p, nil); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("db: %s: %w", p, err)
		}
	}
	return conn, nil
}

// SerializeFloat32 encodes v as the little-endian float32 blob vec0 stores,
// like sqlite_vec.serialize_float32.
func SerializeFloat32(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

// Querier is what a *sql.DB, a *sql.Tx or a *sql.Conn offers: callers take it to run in the caller's transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Collect scans every row of rows with scan, closes rows and returns the scanned values. Like a Python list
// the result is never nil, so it encodes as [] when there are no rows.
func Collect[T any](rows *sql.Rows, scan func(*sql.Rows) (T, error)) ([]T, error) {
	defer func() { _ = rows.Close() }()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
