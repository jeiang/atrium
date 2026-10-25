package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/settings"
)

func BackupCommand() *cli.Command {
	return &cli.Command{
		Name:  "backup",
		Help:  "write one archive holding a consistent copy of the database and the uploaded files; safe while atrium runs",
		Setup: backupSetup,
	}
}

func removeSidecars(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + suffix)
	}
}

func createExclusive(path string) error {
	removeSidecars(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func backupSetup(a *cli.Args) cli.Handler {
	dest := a.Arg("dest", cli.Opt{Help: "path of the new archive; must not exist"})
	return func(e *cli.Env) int {
		cfg, err := config.Load(config.OS)
		if err != nil {
			return cli.Fail(e, err)
		}
		if _, err := os.Stat(cfg.DBPath); err != nil {
			return cli.Fail(e, fmt.Errorf("database %s: %w", cfg.DBPath, err))
		}
		key, err := dbkey.Load(config.OS, settings.Default)
		if err != nil {
			return cli.Fail(e, err)
		}
		if _, err := os.Lstat(*dest); err == nil {
			return cli.Fail(e, fmt.Errorf("backup destination %s already exists", *dest))
		}
		count, err := writeBackup(e.Ctx, cfg, key, *dest)
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

func writeBackup(ctx context.Context, cfg config.Config, key db.Key, dest string) (count int, err error) {
	d, err := db.Connect(cfg.DBPath, db.WithKey(key))
	if err != nil {
		return 0, err
	}
	defer func() { _ = d.Close() }()
	started := float64(time.Now().UnixNano()) / 1e9
	defer func() { passrun.Record(context.WithoutCancel(ctx), d, passrun.Backup, started, err) }()
	partial := dest + ".partial"
	snapshot := partial + ".db"
	for _, p := range []string{partial, snapshot} {
		if err := createExclusive(p); err != nil {
			return 0, err
		}
		defer removeSidecars(p)
	}
	if err := d.BackupTo(ctx, snapshot, key); err != nil {
		return 0, err
	}
	header, err := snapshotHeader(ctx, snapshot, key)
	if err != nil {
		return 0, err
	}
	files, err := listFiles(cfg.FilesDir())
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		return 0, err
	}
	header.Files = len(files)
	header.Stored = info.Size()
	for _, f := range files {
		header.Stored += storedSize(key, f.size)
	}
	out, err := os.OpenFile(partial, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	err = writeArchive(out, header, key, snapshot, files)
	if err == nil {
		err = out.Sync()
	}
	if err = errors.Join(err, out.Close()); err != nil {
		return 0, err
	}
	if err := verifyWritten(ctx, partial, key); err != nil {
		return 0, fmt.Errorf("backup verification: %w", err)
	}
	if err := os.Link(partial, dest); err != nil {
		return 0, err
	}
	return len(files), nil
}

func snapshotHeader(ctx context.Context, snapshot string, key db.Key) (Header, error) {
	c, err := db.OpenFile(snapshot, key)
	if err != nil {
		return Header{}, err
	}
	defer func() { _ = c.Close() }()
	if err := db.IntegrityCheck(ctx, c); err != nil {
		return Header{}, fmt.Errorf("backup verification: %w", err)
	}
	schema, err := db.SchemaVersion(ctx, c)
	if err != nil {
		return Header{}, err
	}
	h := Header{Format: formatVersion, CreatedAt: time.Now().UTC().Format(time.RFC3339), Schema: schema, Encrypted: !key.IsZero()}
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
	h.Salt = hex.EncodeToString(salt)
	h.KeyID = dbkey.ID(key)
	h.Verifier = dbkey.BackupVerifier(key)
	for _, id := range retired {
		if id != h.KeyID && !slices.Contains(h.KeyIDs, id) {
			h.KeyIDs = append(h.KeyIDs, id)
		}
	}
	h.KeyIDs = append(h.KeyIDs, h.KeyID)
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
