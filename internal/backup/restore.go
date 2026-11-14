package backup

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings"
)

const maxKeyInput = 1024

type Report struct {
	Header Header
	Files  int
	From   int
	To     int
	Tables []db.VirtualTable
	KNN    []string
}

type KeyFile struct {
	Given bool
	Raw   []byte
}

type Prepared struct {
	Report Report
	Key    db.Key
	dir    string
}

func (p *Prepared) Discard() { _ = os.RemoveAll(p.dir) }

func Prepare(ctx context.Context, cfg config.Config, configured db.Key, archive string, keyFile KeyFile, verify bool) (*Prepared, error) {
	o, err := openArchive(archive)
	if err != nil {
		return nil, fmt.Errorf("restore %s: %w", archive, err)
	}
	firstHead := o.rawHead
	key, err := archiveKey(o.header, configured, keyFile)
	if err != nil {
		_ = o.Close()
		return nil, err
	}
	if o.header.Schema > db.LatestSchemaVersion() {
		_ = o.Close()
		return nil, fmt.Errorf("%w (archive schema %04d, newest known %04d); nothing was changed", ErrNewerSchema, o.header.Schema, db.LatestSchemaVersion())
	}
	stage, err := stagingDir(cfg, archive, verify)
	if err != nil {
		_ = o.Close()
		return nil, err
	}
	p := &Prepared{Key: key, dir: stage}
	if _, err := o.unpack(ctx, key, ""); err != nil {
		_ = o.Close()
		p.Discard()
		return nil, fmt.Errorf("restore %s: %w; nothing was changed", archive, err)
	}
	_ = o.Close()
	if o, err = openArchive(archive); err != nil {
		p.Discard()
		return nil, fmt.Errorf("restore %s: %w", archive, err)
	}
	defer func() { _ = o.Close() }()
	if !bytes.Equal(o.rawHead, firstHead) {
		p.Discard()
		return nil, fmt.Errorf("restore %s: %w: the archive changed while it was being read; nothing was changed", archive, ErrTampered)
	}
	entries, err := o.unpack(ctx, key, stage)
	if err != nil {
		p.Discard()
		return nil, fmt.Errorf("restore %s: %w; nothing was changed", archive, err)
	}
	rep, err := checkStaged(ctx, filepath.Join(stage, dbEntry), key)
	if err != nil {
		p.Discard()
		return nil, fmt.Errorf("restore %s: %w; nothing was changed", archive, err)
	}
	rep.Header = o.header
	for name, entry := range entries {
		if !entry.Dir && name != dbEntry {
			rep.Files++
		}
	}
	p.Report = rep
	return p, nil
}

func (p *Prepared) Install(ctx context.Context, cfg config.Config, configured db.Key, replaceUnopenable bool) ([]string, error) {
	return install(ctx, cfg, configured, p.Key, p.dir, replaceUnopenable)
}

func stagingDir(cfg config.Config, archive string, verify bool) (string, error) {
	dir := filepath.Dir(cfg.DBPath)
	if verify {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			dir = filepath.Dir(archive)
		}
	} else if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	return os.MkdirTemp(dir, ".atrium-restore-")
}

func parseKeyFile(raw []byte) (db.Key, error) {
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

func archiveKey(h Header, configured db.Key, keyFile KeyFile) (db.Key, error) {
	if h.Seal == nil {
		if keyFile.Given {
			return db.Key{}, errors.New("--key-file was given but the archive is plaintext")
		}
		if !configured.IsZero() {
			return db.Key{}, ErrPlaintextKeyed
		}
		return db.Key{}, nil
	}
	key := configured
	if keyFile.Given {
		var err error
		if key, err = parseKeyFile(keyFile.Raw); err != nil {
			return db.Key{}, err
		}
	}
	switch {
	case key.IsZero():
		return db.Key{}, fmt.Errorf("%w: the archive is encrypted under key id %s and no key is configured; set the %s setting or pass --key-file", ErrArchiveKey, h.Seal.KeyID, db.KeySetting)
	case dbkey.ID(key) != h.Seal.KeyID:
		return db.Key{}, fmt.Errorf("%w: the archive is encrypted under key id %s (key ids it records: %s) but the key given has id %s; pass --key-file with the key whose id is %s", ErrArchiveKey, h.Seal.KeyID, strings.Join(h.Seal.KeyIDs, ", "), dbkey.ID(key), h.Seal.KeyID)
	}
	if err := checkKey(h, key); err != nil {
		return db.Key{}, err
	}
	return key, nil
}

func checkStaged(ctx context.Context, staged string, key db.Key) (Report, error) {
	rep, err := inspectStaged(ctx, staged, key)
	if err != nil {
		return rep, err
	}
	return rep, migrateStaged(ctx, staged, key, &rep)
}

func inspectStaged(ctx context.Context, staged string, key db.Key) (rep Report, err error) {
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
	if rep.From, err = db.SchemaVersion(ctx, c); err != nil {
		return rep, fmt.Errorf("schema version: %w", err)
	}
	if rep.Tables, err = db.VirtualTables(ctx, c); err != nil {
		return rep, fmt.Errorf("table counts: %w", err)
	}
	for _, t := range rep.Tables {
		if err := db.SpotCheckKNN(ctx, c, t); err != nil {
			return rep, fmt.Errorf("restored database: %w", err)
		}
		if t.Kind == "vec0" && t.Rows > 0 {
			rep.KNN = append(rep.KNN, t.Name)
		}
	}
	return rep, nil
}

func migrateStaged(ctx context.Context, staged string, key db.Key, rep *Report) error {
	m, err := db.Connect(staged, db.WithKey(key))
	if err != nil {
		return err
	}
	err = db.Migrate(ctx, m, staged)
	if err == nil {
		err = settings.NewStore(settings.From(ctx), m, m).Load(ctx)
	}
	if err = errors.Join(wrapMigrate(err), m.Close()); err != nil {
		return err
	}
	return settleStaged(ctx, staged, key, rep)
}

func wrapMigrate(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("migrating the restored database: %w", err)
}

func settleStaged(ctx context.Context, staged string, key db.Key, rep *Report) (err error) {
	c, err := db.OpenFile(staged, key)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if err := db.IntegrityCheck(ctx, c); err != nil {
		return fmt.Errorf("migrated database: %w", err)
	}
	if rep.To, err = db.SchemaVersion(ctx, c); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "PRAGMA journal_mode = DELETE")
	return err
}

func holdExisting(ctx context.Context, path string, replaceUnopenable bool, keys ...db.Key) (func(), bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return func() {}, false, nil
	}
	var last error
	for _, k := range keys {
		held, err := db.OpenFile(path, k)
		if err != nil {
			last = err
			continue
		}
		if err := db.HoldExclusive(ctx, held); err != nil {
			_ = held.Close()
			if errors.Is(err, db.ErrInUse) {
				return nil, false, &Refusal{fmt.Errorf("restore refused: %w; the database and files are unchanged", err)}
			}
			last = err
			continue
		}
		return func() { _ = held.Close() }, true, nil
	}
	if replaceUnopenable {
		return func() {}, false, nil
	}
	return nil, false, &Refusal{fmt.Errorf("restore refused: the existing database %s cannot be opened and locked with the configured key or the archive's key (%v), so a running instance cannot be ruled out; fix the key or pass --replace-unopenable to replace it, which keeps the old database aside; the database and files are unchanged", path, last)}
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

func install(ctx context.Context, cfg config.Config, configured, key db.Key, stage string, replaceUnopenable bool) ([]string, error) {
	path := cfg.DBPath
	exclusion, err := db.TryServeLock(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("restore refused: %w; the database and files are unchanged", err)
	}
	defer func() { _ = exclusion.Close() }()
	lock, err := db.Exclusive(ctx, path, db.ShortLockWait)
	if err != nil {
		var timeout *db.LockTimeout
		if errors.As(err, &timeout) {
			return nil, &Refusal{fmt.Errorf("restore refused: %w; stop the atrium units and timers that use the database first", err)}
		}
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	release, _, err := holdExisting(ctx, path, replaceUnopenable, configured, key)
	if err != nil {
		return nil, err
	}
	defer release()
	owner, err := os.Stat(path)
	dbMode := fs.FileMode(0o600)
	if err == nil {
		dbMode = owner.Mode().Perm()
	} else if owner, err = os.Stat(filepath.Dir(path)); err != nil {
		return nil, err
	}
	stagedFiles := filepath.Join(stage, "files")
	if err := os.MkdirAll(stagedFiles, 0o700); err != nil {
		return nil, err
	}
	dirMode := fs.FileMode(0o755)
	if info, err := os.Stat(cfg.FilesDir()); err == nil {
		dirMode = info.Mode().Perm()
	}
	stagedDB := filepath.Join(stage, dbEntry)
	if err := os.Chmod(stagedDB, dbMode); err != nil {
		return nil, err
	}
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (p == stage || p == stagedFiles) {
			if err := os.Chmod(p, dirMode); err != nil {
				return err
			}
		}
		if err := chownLike(p, owner); err != nil {
			return err
		}
		return syncPath(p)
	})
	if err != nil {
		return nil, err
	}
	return swap(path, cfg.FilesDir(), stagedDB, stagedFiles)
}

func syncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

type undoStack []func()

func (u undoStack) run() {
	for i := len(u) - 1; i >= 0; i-- {
		u[i]()
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func swap(dbPath, filesDir, stagedDB, stagedFiles string) (moved []string, err error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	var undo undoStack
	defer func() {
		if err != nil {
			undo.run()
			moved = nil
		}
	}()
	move := func(from, to string) error {
		if exists(to) {
			return fmt.Errorf("restore: %s already exists; the database and files are unchanged", to)
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		undo = append(undo, func() { _ = os.Rename(to, from) })
		return nil
	}
	if exists(filesDir) {
		aside := filesDir + ".pre-restore-" + stamp
		if err := move(filesDir, aside); err != nil {
			return nil, err
		}
		moved = append(moved, aside)
	}
	if err := move(stagedFiles, filesDir); err != nil {
		return nil, err
	}
	asideDB := dbPath + ".pre-restore-" + stamp
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if !exists(dbPath + suffix) {
			continue
		}
		if err := move(dbPath+suffix, asideDB+suffix); err != nil {
			return nil, err
		}
		if suffix == "" {
			moved = append(moved, asideDB)
		}
	}
	if err := move(stagedDB, dbPath); err != nil {
		return nil, err
	}
	if err := syncPath(filepath.Dir(dbPath)); err != nil {
		return nil, err
	}
	return moved, nil
}
