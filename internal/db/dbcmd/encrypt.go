package dbcmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
)

const knnSample = 10

func (s *service) encrypt(a *cli.Args) cli.Handler {
	a.RejectExtras("db encrypt takes no arguments; the key comes from the " + db.KeySetting + " setting")
	return func(e *cli.Env) int {
		key, err := s.currentKey()
		if err != nil {
			return cli.Fail(e, err)
		}
		if key.IsZero() {
			return cli.Fail(e, errors.New("db encrypt: no database key is configured; set "+db.KeySetting+" or the "+db.KeyCredential+" credential first"))
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
		held, err := db.OpenFile(path, db.Key{})
		if errors.Is(err, db.ErrKeyMissing) {
			return cli.Fail(e, fmt.Errorf("db encrypt: %s is already encrypted; the database is unchanged", path))
		}
		if err != nil {
			return cli.Fail(e, err)
		}
		defer func() { _ = held.Close() }()
		if err := db.HoldExclusive(e.Ctx, held); err != nil {
			if errors.Is(err, db.ErrInUse) {
				return cli.Fail(e, fmt.Errorf("db encrypt: %w; the database is unchanged", err))
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
		tmp := path + ".encrypt"
		if err := exclusiveTemp(tmp, info.Mode().Perm()); err != nil {
			return cli.Fail(e, err)
		}
		defer removeCopy(tmp)
		if err := chownLike(tmp, info); err != nil {
			return cli.Fail(e, err)
		}
		if err := db.Export(e.Ctx, held, tmp, key); err != nil {
			return cli.Fail(e, err)
		}
		want, err := db.TableCounts(e.Ctx, held)
		if err != nil {
			return cli.Fail(e, err)
		}
		if err := verifyEncrypted(e.Ctx, held, tmp, key, want); err != nil {
			return cli.Fail(e, fmt.Errorf("db encrypt: verification of the encrypted copy failed, the database is unchanged: %w", err))
		}
		if err := os.Rename(tmp, path); err != nil {
			return cli.Fail(e, err)
		}
		if err := syncDir(filepath.Dir(path)); err != nil {
			return cli.Fail(e, err)
		}
		var rows int64
		for _, n := range want {
			rows += n
		}
		_, _ = fmt.Fprintf(e.Stdout, "encrypted %s: %d tables, %d rows, key id %s\n", path, len(want), rows, dbkey.ID(key))
		return cli.ExitOK
	}
}

func verifyEncrypted(ctx context.Context, src *sql.DB, path string, key db.Key, want map[string]int64) error {
	if err := verifyCopy(ctx, path, key, want, ""); err != nil {
		return err
	}
	if _, err := db.OpenFile(path, db.Key{}); !errors.Is(err, db.ErrKeyMissing) {
		return fmt.Errorf("the copy does not have an encrypted header (%v)", err)
	}
	c, err := db.OpenFile(path, key)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	tables, err := db.VirtualTables(ctx, src)
	if err != nil {
		return err
	}
	for _, t := range tables {
		before, err := db.Neighbours(ctx, src, t, knnSample)
		if err != nil {
			return err
		}
		after, err := db.Neighbours(ctx, c, t, knnSample)
		if err != nil {
			return err
		}
		if !slices.Equal(before, after) {
			return fmt.Errorf("knn %s: nearest rows %v in the copy, %v in the source", t.Name, after, before)
		}
	}
	return nil
}
