package backup

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

const maxKeyInput = 1024

func RestoreCommand() *cli.Command {
	return &cli.Command{
		Name:  "restore",
		Help:  "replace the database and uploaded files with the contents of a backup archive; stop the atrium units and timers that use the database first",
		Setup: restoreSetup,
	}
}

type report struct {
	header   Header
	files    int
	from, to int
	tables   []db.VirtualTable
	knn      []string
}

func restoreSetup(a *cli.Args) cli.Handler {
	verify := a.Bool("verify", cli.Opt{Help: "check the archive in a temporary location and change nothing"})
	keyFile := a.String("key-file", "", cli.Opt{Metavar: "PATH", Help: "file holding the 64-hex-digit key the archive was made under, when it differs from the configured key"})
	archive := a.Arg("archive", cli.Opt{Help: "path of the backup archive"})
	return func(e *cli.Env) int {
		cfg, err := config.Load(config.OS)
		if err != nil {
			return cli.Fail(e, err)
		}
		configured, err := dbkey.Load(config.OS, settings.Default)
		if err != nil {
			return cli.Fail(e, err)
		}
		o, err := openArchive(*archive)
		if err != nil {
			return cli.Fail(e, fmt.Errorf("restore %s: %w", *archive, err))
		}
		defer func() { _ = o.Close() }()
		key, err := archiveKey(o.header, configured, *keyFile)
		if err != nil {
			return cli.Fail(e, err)
		}
		if o.header.Schema > db.LatestSchemaVersion() {
			return cli.Fail(e, fmt.Errorf("%w (archive schema %04d, newest known %04d); nothing was changed", ErrNewerSchema, o.header.Schema, db.LatestSchemaVersion()))
		}
		stage, err := stagingDir(cfg, *verify)
		if err != nil {
			return cli.Fail(e, err)
		}
		defer func() { _ = os.RemoveAll(stage) }()
		entries, err := o.unpack(e.Ctx, key, stage)
		if err != nil {
			return cli.Fail(e, fmt.Errorf("restore %s: %w; nothing was changed", *archive, err))
		}
		rep, err := checkStaged(e.Ctx, filepath.Join(stage, dbEntry), key)
		if err != nil {
			return cli.Fail(e, fmt.Errorf("restore %s: %w; nothing was changed", *archive, err))
		}
		rep.header, rep.files = o.header, len(entries)-1
		printReport(e.Stdout, *archive, rep)
		if *verify {
			_, _ = fmt.Fprintln(e.Stdout, "verified; nothing was written")
			return cli.ExitOK
		}
		if err := install(e.Ctx, cfg, configured, key, stage); err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "restored %s\n", cfg.DBPath)
		if !key.IsZero() && dbkey.ID(key) != dbkey.ID(configured) {
			_, _ = fmt.Fprintf(e.Stdout, "the restored database is encrypted under key id %s, not the configured key; configure that key before starting atrium\n", dbkey.ID(key))
		}
		return cli.ExitOK
	}
}

func stagingDir(cfg config.Config, verify bool) (string, error) {
	dir := filepath.Dir(cfg.DBPath)
	if verify {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			dir = ""
		}
	} else if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, ".atrium-restore-")
}

func readKeyFile(path string) (db.Key, error) {
	f, err := os.Open(path)
	if err != nil {
		return db.Key{}, errors.New("--key-file could not be opened")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyInput+1))
	if err != nil {
		return db.Key{}, err
	}
	if len(raw) > maxKeyInput {
		return db.Key{}, fmt.Errorf("--key-file: %w", db.ErrInvalidKey)
	}
	k, err := db.ParseKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return db.Key{}, fmt.Errorf("--key-file: %w", err)
	}
	logx.AddSecret(hex.EncodeToString(k.Raw()))
	return k, nil
}

func archiveKey(h Header, configured db.Key, keyFile string) (db.Key, error) {
	if !h.Encrypted {
		if !configured.IsZero() {
			return db.Key{}, ErrPlaintextKeyed
		}
		return db.Key{}, nil
	}
	key := configured
	if keyFile != "" {
		var err error
		if key, err = readKeyFile(keyFile); err != nil {
			return db.Key{}, err
		}
	}
	switch {
	case key.IsZero():
		return db.Key{}, fmt.Errorf("%w: the archive is encrypted under key id %s and no key is configured; set the %s setting or pass --key-file", ErrArchiveKey, h.KeyID, db.KeySetting)
	case dbkey.ID(key) != h.KeyID:
		return db.Key{}, fmt.Errorf("%w: the archive is encrypted under key id %s (key ids it records: %s) but the key given has id %s; pass --key-file with the key whose id is %s", ErrArchiveKey, h.KeyID, strings.Join(h.KeyIDs, ", "), dbkey.ID(key), h.KeyID)
	}
	if err := checkKey(h, key); err != nil {
		return db.Key{}, err
	}
	return key, nil
}

func checkStaged(ctx context.Context, staged string, key db.Key) (report, error) {
	rep, err := inspectStaged(ctx, staged, key)
	if err != nil {
		return rep, err
	}
	return rep, migrateStaged(ctx, staged, key, &rep)
}

func inspectStaged(ctx context.Context, staged string, key db.Key) (rep report, err error) {
	c, err := db.OpenFile(staged, key)
	if err != nil {
		return rep, fmt.Errorf("open the restored database: %w", err)
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if err := db.RefuseFutureSchema(ctx, c); err != nil {
		return rep, fmt.Errorf("schema check: %w", err)
	}
	if err := db.IntegrityCheck(ctx, c); err != nil {
		return rep, fmt.Errorf("restored database: %w", err)
	}
	if rep.from, err = db.SchemaVersion(ctx, c); err != nil {
		return rep, fmt.Errorf("schema version: %w", err)
	}
	if rep.tables, err = db.VirtualTables(ctx, c); err != nil {
		return rep, fmt.Errorf("table counts: %w", err)
	}
	for _, t := range rep.tables {
		if err := db.SpotCheckKNN(ctx, c, t); err != nil {
			return rep, fmt.Errorf("restored database: %w", err)
		}
		if t.Kind == "vec0" && t.Rows > 0 {
			rep.knn = append(rep.knn, t.Name)
		}
	}
	return rep, nil
}

func migrateStaged(ctx context.Context, staged string, key db.Key, rep *report) (err error) {
	m, err := db.Connect(staged, db.WithKey(key))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, m.Close()) }()
	if err := db.Migrate(ctx, m, staged); err != nil {
		return fmt.Errorf("migrating the restored database: %w", err)
	}
	if err := db.IntegrityCheck(ctx, m.Read); err != nil {
		return err
	}
	if rep.to, err = db.SchemaVersion(ctx, m.Read); err != nil {
		return err
	}
	_, err = m.Write.ExecContext(ctx, "PRAGMA journal_mode = DELETE")
	return err
}

func printReport(w io.Writer, archive string, rep report) {
	h := rep.header
	_, _ = fmt.Fprintf(w, "archive: %s\ncreated: %s\n", archive, h.CreatedAt)
	if rep.from == rep.to {
		_, _ = fmt.Fprintf(w, "schema: %04d\n", rep.to)
	} else {
		_, _ = fmt.Fprintf(w, "schema: %04d, migrated to %04d\n", rep.from, rep.to)
	}
	if h.Encrypted {
		_, _ = fmt.Fprintf(w, "encryption: encrypted, key id %s (key ids: %s)\n", h.KeyID, strings.Join(h.KeyIDs, ", "))
	} else {
		_, _ = fmt.Fprintln(w, "encryption: plaintext")
	}
	_, _ = fmt.Fprintln(w, "integrity_check: ok")
	for _, t := range rep.tables {
		_, _ = fmt.Fprintf(w, "%s %s: %d\n", t.Kind, t.Name, t.Rows)
	}
	for _, name := range rep.knn {
		_, _ = fmt.Fprintf(w, "knn %s: ok\n", name)
	}
	_, _ = fmt.Fprintf(w, "files: %d\n", rep.files)
}

func isKeyError(err error) bool {
	return errors.Is(err, db.ErrWrongKey) || errors.Is(err, db.ErrKeyMissing) || errors.Is(err, db.ErrPlaintextDB)
}

func holdExisting(ctx context.Context, path string, keys ...db.Key) (func(), error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return func() {}, nil
	}
	for _, k := range keys {
		held, err := db.OpenFile(path, k)
		if isKeyError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := db.HoldExclusive(ctx, held); err != nil {
			_ = held.Close()
			if errors.Is(err, db.ErrInUse) {
				return nil, fmt.Errorf("restore refused: %w; the database and files are unchanged", err)
			}
			return nil, err
		}
		return func() { _ = held.Close() }, nil
	}
	return func() {}, nil
}

func chownLike(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Lchown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("give the restored %s the owner of the state directory: %w", filepath.Base(path), err)
	}
	return nil
}

func install(ctx context.Context, cfg config.Config, configured, key db.Key, stage string) error {
	path := cfg.DBPath
	lock, err := db.Exclusive(ctx, path, db.ShortLockWait)
	if err != nil {
		var timeout *db.LockTimeout
		if errors.As(err, &timeout) {
			return fmt.Errorf("restore refused: %w; stop the atrium units and timers that use the database first", err)
		}
		return err
	}
	defer func() { _ = lock.Close() }()
	release, err := holdExisting(ctx, path, configured, key)
	if err != nil {
		return err
	}
	defer release()
	owner, err := os.Stat(path)
	dbMode := fs.FileMode(0o600)
	if err == nil {
		dbMode = owner.Mode().Perm()
	} else if owner, err = os.Stat(filepath.Dir(path)); err != nil {
		return err
	}
	stagedFiles := filepath.Join(stage, "files")
	if err := os.MkdirAll(stagedFiles, 0o700); err != nil {
		return err
	}
	dirMode := fs.FileMode(0o755)
	if info, err := os.Stat(cfg.FilesDir()); err == nil {
		dirMode = info.Mode().Perm()
	}
	stagedDB := filepath.Join(stage, dbEntry)
	if err := os.Chmod(stagedDB, dbMode); err != nil {
		return err
	}
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if err := os.Chmod(p, dirMode); err != nil {
				return err
			}
		}
		return chownLike(p, owner)
	})
	if err != nil {
		return err
	}
	return swap(path, cfg.FilesDir(), stagedDB, stagedFiles)
}

func swap(dbPath, filesDir, stagedDB, stagedFiles string) error {
	replaced := filesDir + ".replaced"
	if err := os.RemoveAll(replaced); err != nil {
		return err
	}
	hadFiles := true
	if err := os.Rename(filesDir, replaced); errors.Is(err, fs.ErrNotExist) {
		hadFiles = false
	} else if err != nil {
		return err
	}
	if err := os.Rename(stagedFiles, filesDir); err != nil {
		if hadFiles {
			_ = os.Rename(replaced, filesDir)
		}
		return err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(stagedDB, dbPath); err != nil {
		if hadFiles {
			_ = os.RemoveAll(filesDir)
			_ = os.Rename(replaced, filesDir)
		}
		return err
	}
	if err := syncDir(filepath.Dir(dbPath)); err != nil {
		return err
	}
	return os.RemoveAll(replaced)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
