package dbcmd

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

const maxKeyInput = 1024

func Command() *cli.Command {
	s := &service{env: config.OS}
	return &cli.Command{
		Name: "db",
		Commands: []*cli.Command{
			{Name: "rekey", Help: "re-encrypt the database under a new key read from a file or standard input", Setup: s.rekey},
			{Name: "backup", Help: "write a consistent copy of the database, encrypted like the source", Setup: s.backup},
			{Name: "shell", Help: "run SQL statements from standard input against the database", Setup: s.shell},
			{Name: "check", Help: "verify database integrity and report FTS5 and vec0 table counts", Setup: s.check},
		},
	}
}

type service struct{ env config.Env }

func (s *service) path() (string, error) {
	cfg, err := config.Load(s.env)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return "", fmt.Errorf("database %s: %w", cfg.DBPath, err)
	}
	return cfg.DBPath, nil
}

func (s *service) open(e *cli.Env) (*db.DB, string, int) {
	path, err := s.path()
	if err != nil {
		return nil, "", cli.Fail(e, err)
	}
	d, err := db.Connect(path)
	if err != nil {
		return nil, "", cli.Fail(e, err)
	}
	return d, path, cli.ExitOK
}

func (s *service) currentKey() (db.Key, error) {
	return dbkey.Load(s.env, settings.Default)
}

func readKey(path string, stdin io.Reader) (db.Key, error) {
	r := stdin
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return db.Key{}, fmt.Errorf("new key file: %w", err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxKeyInput+1))
	if err != nil {
		return db.Key{}, err
	}
	if len(raw) > maxKeyInput {
		return db.Key{}, db.ErrInvalidKey
	}
	return db.ParseKey(strings.TrimSpace(string(raw)))
}

func removeCopy(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func exclusiveTemp(path string, perm os.FileMode) error {
	removeCopy(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	return f.Close()
}

func tableCounts(ctx context.Context, q db.Querier) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return nil, err
	}
	names, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var n string
		err := r.Scan(&n)
		return n, err
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(names))
	for _, n := range names {
		var c int64
		if err := q.QueryRowContext(ctx, "SELECT count(*) FROM \""+strings.ReplaceAll(n, "\"", "\"\"")+"\"").Scan(&c); err != nil {
			return nil, fmt.Errorf("count %s: %w", n, err)
		}
		out[n] = c
	}
	return out, nil
}

func integrity(ctx context.Context, q db.Querier) error {
	rows, err := q.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	lines, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var l string
		err := r.Scan(&l)
		return l, err
	})
	if err != nil {
		return err
	}
	if len(lines) != 1 || lines[0] != "ok" {
		return fmt.Errorf("integrity_check: %s", strings.Join(lines, "; "))
	}
	return nil
}

func sameCounts(want, got map[string]int64, ignore string) error {
	for name, n := range want {
		if name != ignore && got[name] != n {
			return fmt.Errorf("table %s has %d rows in the copy, %d in the source", name, got[name], n)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok && name != ignore {
			return fmt.Errorf("table %s exists only in the copy", name)
		}
	}
	return nil
}

func verifyCopy(ctx context.Context, path string, key db.Key, want map[string]int64, ignore string) error {
	c, err := db.OpenFile(path, key)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := integrity(ctx, c); err != nil {
		return err
	}
	if want == nil {
		return nil
	}
	got, err := tableCounts(ctx, c)
	if err != nil {
		return err
	}
	return sameCounts(want, got, ignore)
}

func (s *service) backup(a *cli.Args) cli.Handler {
	dest := a.Arg("dest", cli.Opt{Help: "path of the new backup file; must not exist"})
	return func(e *cli.Env) int {
		d, _, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		key, err := s.currentKey()
		if err != nil {
			return cli.Fail(e, err)
		}
		if _, err := os.Lstat(*dest); err == nil {
			return cli.Fail(e, fmt.Errorf("backup destination %s already exists", *dest))
		}
		tmp := *dest + ".partial"
		if err := exclusiveTemp(tmp, 0o600); err != nil {
			return cli.Fail(e, err)
		}
		defer removeCopy(tmp)
		if err := d.BackupTo(e.Ctx, tmp, key); err != nil {
			return cli.Fail(e, err)
		}
		if err := verifyCopy(e.Ctx, tmp, key, nil, ""); err != nil {
			return cli.Fail(e, fmt.Errorf("backup verification: %w", err))
		}
		if err := os.Link(tmp, *dest); err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "backup written to %s (%s)\n", *dest, encryption(d, key))
		return cli.ExitOK
	}
}

func encryption(d *db.DB, key db.Key) string {
	if !d.Encrypted() {
		return "plaintext"
	}
	return "encrypted, key id " + dbkey.ID(key)
}

func (s *service) check(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		d, path, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		key, err := s.currentKey()
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "database: %s\nencryption: %s\n", path, encryption(d, key))
		if err := integrity(e.Ctx, d.Read); err != nil {
			_, _ = fmt.Fprintf(e.Stdout, "integrity_check: FAILED\n")
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "integrity_check: ok\n")
		rows, err := d.Read.QueryContext(e.Ctx, "SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'table' AND sql LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name")
		if err != nil {
			return cli.Fail(e, err)
		}
		type vt struct{ name, kind string }
		tables, err := db.Collect(rows, func(r *sql.Rows) (vt, error) {
			var name, text string
			if err := r.Scan(&name, &text); err != nil {
				return vt{}, err
			}
			upper := strings.ToUpper(text)
			switch {
			case strings.Contains(upper, "USING FTS5"):
				return vt{name, "fts5"}, nil
			case strings.Contains(upper, "USING VEC0"):
				return vt{name, "vec0"}, nil
			}
			return vt{}, nil
		})
		if err != nil {
			return cli.Fail(e, err)
		}
		for _, t := range tables {
			if t.kind == "" {
				continue
			}
			var n int64
			if err := d.Read.QueryRowContext(e.Ctx, "SELECT count(*) FROM \""+strings.ReplaceAll(t.name, "\"", "\"\"")+"\"").Scan(&n); err != nil {
				return cli.Fail(e, fmt.Errorf("count %s: %w", t.name, err))
			}
			_, _ = fmt.Fprintf(e.Stdout, "%s %s: %d\n", t.kind, t.name, n)
		}
		return cli.ExitOK
	}
}

func (s *service) rekey(a *cli.Args) cli.Handler {
	keyFile := a.String("new-key-file", "", cli.Opt{Metavar: "PATH", Help: "file holding the new 64-hex-digit key; standard input is read when omitted"})
	a.RejectExtras("the new key must come from --new-key-file or standard input, not the command line")
	return func(e *cli.Env) int {
		old, err := s.currentKey()
		if err != nil {
			return cli.Fail(e, err)
		}
		if old.IsZero() {
			return cli.Fail(e, errors.New("db rekey: no database key is configured, so the database is plaintext; rekey only rotates the key of an encrypted database (set "+db.KeySetting+" first)"))
		}
		next, err := readKey(*keyFile, e.Stdin)
		if err != nil {
			return cli.Fail(e, fmt.Errorf("db rekey: new key: %w", err))
		}
		logx.AddSecret(hex.EncodeToString(next.Raw()))
		oldID, newID := dbkey.ID(old), dbkey.ID(next)
		if oldID == newID {
			return cli.Fail(e, errors.New("db rekey: the new key is the current key"))
		}
		path, err := s.path()
		if err != nil {
			return cli.Fail(e, err)
		}
		d, err := db.Connect(path)
		if err != nil {
			return cli.Fail(e, err)
		}
		closed := false
		defer func() {
			if !closed {
				_ = d.Close()
			}
		}()
		if err := db.Migrate(e.Ctx, d, path); err != nil {
			return cli.Fail(e, err)
		}
		lock, err := db.Exclusive(e.Ctx, path, db.ShortLockWait)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer func() { _ = lock.Close() }()
		info, err := os.Stat(path)
		if err != nil {
			return cli.Fail(e, err)
		}
		tmp := path + ".rekey"
		if err := exclusiveTemp(tmp, info.Mode().Perm()); err != nil {
			return cli.Fail(e, err)
		}
		defer removeCopy(tmp)
		if err := chownLike(tmp, info); err != nil {
			return cli.Fail(e, err)
		}
		if err := d.BackupTo(e.Ctx, tmp, next); err != nil {
			return cli.Fail(e, err)
		}
		want, err := tableCounts(e.Ctx, d.Read)
		if err != nil {
			return cli.Fail(e, err)
		}
		if err := recordRetired(e.Ctx, tmp, next, oldID, newID); err != nil {
			return cli.Fail(e, err)
		}
		if err := verifyCopy(e.Ctx, tmp, next, want, "db_key_history"); err != nil {
			return cli.Fail(e, fmt.Errorf("db rekey: verification of the re-encrypted copy failed, the database is unchanged: %w", err))
		}
		closed = true
		if err := d.Close(); err != nil {
			return cli.Fail(e, err)
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
		if err := os.Rename(tmp, path); err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "rekeyed %s: key id %s -> %s\n", path, oldID, newID)
		return cli.ExitOK
	}
}

func recordRetired(ctx context.Context, path string, key db.Key, oldID, newID string) error {
	c, err := db.OpenFile(path, key)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_, err = c.ExecContext(ctx, "INSERT INTO db_key_history (key_id, replaced_by, retired_at) VALUES (?, ?, CAST(strftime('%s','now') AS REAL))", oldID, newID)
	return err
}
