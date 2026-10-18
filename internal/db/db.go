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
)

func connPragmas(foreignKeys bool) []string {
	fk := "PRAGMA foreign_keys = OFF"
	if foreignKeys {
		fk = "PRAGMA foreign_keys = ON"
	}
	return []string{"PRAGMA busy_timeout = 30000", fk, "PRAGMA synchronous = FULL"}
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
	if _, err := w.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("db: %s: %w", path, err)
	}
	r := Open(path, append(pragmas, "PRAGMA query_only = ON")...)
	return &DB{Write: w, Read: r}, nil
}

func (d *DB) Close() error {
	return errors.Join(d.Read.Close(), d.Write.Close())
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

// Exclusive blocks until it holds flock(LOCK_EX) on path+".lock", which is
// opened in append mode and never deleted.
func Exclusive(path string) (*Lock, error) {
	lock := path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o777); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("db: flock %s: %w", lock, err)
	}
	return &Lock{f: f}, nil
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
