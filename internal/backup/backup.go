package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/settings"
)

func BackupCommand() *cli.Command {
	return &cli.Command{
		Name:  "backup",
		Help:  "write one archive holding a consistent copy of the database and the uploaded files; safe while atrium runs",
		Setup: backupSetup,
	}
}

func refuseDestination(cfg config.Config, dest string) error {
	if filepath.Clean(dest) == filepath.Clean(cfg.DBPath) {
		return fmt.Errorf("backup destination %s is the database itself", dest)
	}
	rel, err := filepath.Rel(filepath.Clean(cfg.FilesDir()), filepath.Clean(dest))
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("backup destination %s is inside %s; choose a path outside the uploaded files", dest, cfg.FilesDir())
	}
	return nil
}

func backupSetup(a *cli.Args) cli.Handler {
	dest := a.Arg("dest", cli.Opt{Help: "path of the new archive; must not exist"})
	return func(e *cli.Env) int {
		cfg, err := config.Load(e.Env)
		if err != nil {
			return cli.Fail(e, err)
		}
		if _, err := os.Stat(cfg.DBPath); err != nil {
			return cli.Fail(e, fmt.Errorf("database %s: %w", cfg.DBPath, err))
		}
		key, err := dbkey.Load(e.Env, settings.From(e.Ctx))
		if err != nil {
			return cli.Fail(e, err)
		}
		if _, err := os.Lstat(*dest); err == nil {
			return cli.Fail(e, fmt.Errorf("backup destination %s already exists", *dest))
		}
		count, err := manual(e.Ctx, cfg, key, *dest)
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "backup written to %s (%s, %d files)\n", *dest, describe(key), count)
		return cli.ExitOK
	}
}

func describe(key db.Key) string {
	if key.IsZero() {
		return "plaintext"
	}
	return "encrypted, key id " + dbkey.ID(key)
}

func writeBackup(ctx context.Context, d *db.DB, cfg config.Config, key db.Key, dest string, forget func(context.Context, db.Querier) error) (int, error) {
	if err := refuseDestination(cfg, dest); err != nil {
		return 0, err
	}
	staged, err := db.Stage(dest, 0o600)
	if err != nil {
		return 0, err
	}
	defer staged.Discard()
	snapshot, err := db.Stage(filepath.Join(filepath.Dir(dest), ".atrium-snapshot"), 0o600)
	if err != nil {
		return 0, err
	}
	defer snapshot.Discard()
	if err := d.BackupTo(ctx, snapshot.Path(), key); err != nil {
		return 0, err
	}
	header, err := snapshotHeader(ctx, snapshot.Path(), key, forget)
	if err != nil {
		return 0, err
	}
	files, err := listFiles(ctx, cfg.FilesDir())
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(snapshot.Path())
	if err != nil {
		return 0, err
	}
	header.Stored = info.Size()
	for _, f := range files {
		if f.dir {
			header.Dirs++
			continue
		}
		header.Files++
		header.Stored += storedSize(header.Seal, f.size)
	}
	out, err := os.OpenFile(staged.Path(), os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	err = writeArchive(ctx, out, header, key, snapshot.Path(), files)
	if err == nil {
		err = out.Sync()
	}
	if err = errors.Join(err, out.Close()); err != nil {
		return 0, err
	}
	if err := verifyWritten(ctx, staged.Path(), key); err != nil {
		return 0, fmt.Errorf("backup verification: %w", err)
	}
	if err := staged.PublishLink(); err != nil {
		return 0, err
	}
	return header.Files, nil
}

func snapshotHeader(ctx context.Context, snapshot string, key db.Key, forget func(context.Context, db.Querier) error) (Header, error) {
	c, err := db.OpenFile(snapshot, key)
	if err != nil {
		return Header{}, err
	}
	defer func() { _ = c.Close() }()
	if err := forget(ctx, c); err != nil {
		return Header{}, err
	}
	if err := db.IntegrityCheck(ctx, c); err != nil {
		return Header{}, fmt.Errorf("backup verification: %w", err)
	}
	schema, err := db.SchemaVersion(ctx, c)
	if err != nil {
		return Header{}, err
	}
	h := Header{Format: formatVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339), Schema: schema}
	if key.IsZero() {
		return h, nil
	}
	retired, err := db.RetiredKeyIDs(ctx, c)
	if err != nil {
		return Header{}, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return Header{}, err
	}
	seal := &Seal{KeyID: dbkey.ID(key), Verifier: dbkey.BackupVerifier(key), Salt: hex.EncodeToString(salt)}
	for _, id := range retired {
		if id != seal.KeyID && !slices.Contains(seal.KeyIDs, id) {
			seal.KeyIDs = append(seal.KeyIDs, id)
		}
	}
	seal.KeyIDs = append(seal.KeyIDs, seal.KeyID)
	h.Seal = seal
	return h, nil
}

func verifyWritten(ctx context.Context, archive string, key db.Key) error {
	o, err := openArchive(archive)
	if err != nil {
		return err
	}
	defer func() { _ = o.Close() }()
	if err := checkKey(o.header, key); err != nil {
		return err
	}
	_, err = o.unpack(ctx, key, "")
	return err
}
