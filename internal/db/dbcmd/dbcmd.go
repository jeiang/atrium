package dbcmd

import (
	"context"
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
			{Name: "encrypt", Help: "encrypt a plaintext database with the configured key into a fresh file, verify it and replace the original; stop the atrium units and timers that use the database first", Setup: s.encrypt},
			{Name: "rekey", Help: "re-encrypt the database under a new key read from a file or standard input; stop the atrium units and timers that use the database first", Setup: s.rekey},
			{Name: "backup", Help: "write a consistent copy of the database, encrypted like the source", Setup: s.backup},
			{Name: "shell", Help: "run SQL statements from standard input against the database", Setup: s.shell},
			{Name: "check", Help: "verify database integrity and report FTS5 and vec0 table counts", Setup: s.check},
			{Name: "import-v1", Help: "copy the learned-rule data from a v1 database into this fresh v2 database", Setup: s.importV1},
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

func (s *service) open(e *cli.Env) (*db.DB, string, db.Key, int) {
	path, err := s.path()
	if err != nil {
		return nil, "", db.Key{}, cli.Fail(e, err)
	}
	key, err := s.currentKey()
	if err != nil {
		return nil, "", db.Key{}, cli.Fail(e, err)
	}
	d, err := db.Connect(path, db.WithKey(key))
	if err != nil {
		return nil, "", db.Key{}, cli.Fail(e, err)
	}
	return d, path, key, cli.ExitOK
}

func (s *service) currentKey() (db.Key, error) {
	return dbkey.Load(s.env, settings.Default)
}

func readKey(path string, stdin io.Reader) (db.Key, error) {
	r := stdin
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return db.Key{}, errors.New("--new-key-file could not be opened")
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

func (s *service) backup(a *cli.Args) cli.Handler {
	dest := a.Arg("dest", cli.Opt{Help: "path of the new backup file; must not exist"})
	return func(e *cli.Env) int {
		d, _, key, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		if _, err := os.Lstat(*dest); err == nil {
			return cli.Fail(e, fmt.Errorf("backup destination %s already exists", *dest))
		}
		staged, err := db.Stage(*dest, 0o600)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer staged.Discard()
		if err := d.BackupTo(e.Ctx, staged.Path(), key); err != nil {
			return cli.Fail(e, err)
		}
		if err := db.Verify(e.Ctx, staged.Path(), key, nil); err != nil {
			return cli.Fail(e, fmt.Errorf("backup verification: %w", err))
		}
		if err := staged.PublishLink(); err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "backup written to %s (%s)\n", *dest, encryption(key))
		return cli.ExitOK
	}
}

func encryption(key db.Key) string {
	if key.IsZero() {
		return "plaintext"
	}
	return "encrypted, key id " + dbkey.ID(key)
}

func (s *service) check(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		path, err := s.path()
		if err != nil {
			return cli.Fail(e, err)
		}
		key, err := s.currentKey()
		if err != nil {
			return cli.Fail(e, err)
		}
		d, err := db.OpenFile(path, key)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer func() { _ = d.Close() }()
		_, _ = fmt.Fprintf(e.Stdout, "database: %s\nencryption: %s\n", path, encryption(key))
		if err := db.IntegrityCheck(e.Ctx, d); err != nil {
			_, _ = fmt.Fprintf(e.Stdout, "integrity_check: FAILED\n")
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "integrity_check: ok\n")
		tables, err := db.VirtualTables(e.Ctx, d)
		if err != nil {
			return cli.Fail(e, err)
		}
		for _, t := range tables {
			_, _ = fmt.Fprintf(e.Stdout, "%s %s: %d\n", t.Kind, t.Name, t.Rows)
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
		lock, err := db.Exclusive(e.Ctx, path, db.ShortLockWait)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer func() { _ = lock.Close() }()
		held, err := db.OpenFile(path, old)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer func() { _ = held.Close() }()
		if err := db.HoldExclusive(e.Ctx, held); err != nil {
			if errors.Is(err, db.ErrInUse) {
				return cli.Fail(e, fmt.Errorf("db rekey: %w; the database is unchanged", err))
			}
			return cli.Fail(e, err)
		}
		if err := db.RefuseFutureSchema(e.Ctx, held); err != nil {
			return cli.Fail(e, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return cli.Fail(e, err)
		}
		staged, err := db.Stage(path, info.Mode().Perm())
		if err != nil {
			return cli.Fail(e, err)
		}
		defer staged.Discard()
		if err := chownLike(staged.Path(), info); err != nil {
			return cli.Fail(e, err)
		}
		if err := db.Backup(e.Ctx, held, staged.Path(), next); err != nil {
			return cli.Fail(e, err)
		}
		want, err := db.TableCounts(e.Ctx, held)
		if err != nil {
			return cli.Fail(e, err)
		}
		if err := db.Verify(e.Ctx, staged.Path(), next, want); err != nil {
			return cli.Fail(e, fmt.Errorf("db rekey: verification of the re-encrypted copy failed, the database is unchanged: %w", err))
		}
		if err := recordRetired(e.Ctx, staged.Path(), next, oldID, newID); err != nil {
			return cli.Fail(e, err)
		}
		if err := staged.PublishRename(); err != nil {
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
	if _, err := c.ExecContext(ctx, db.KeyHistoryDDL); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "INSERT INTO db_key_history (key_id, replaced_by, retired_at) VALUES (?, ?, CAST(strftime('%s','now') AS REAL))", oldID, newID)
	return err
}
