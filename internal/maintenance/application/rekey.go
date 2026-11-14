package application

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/logx"
)

const KeySetting = db.KeySetting

const (
	EncryptName application.Name = "maintenance.encrypt"
	RekeyName   application.Name = "maintenance.rekey"

	maxKeyInput = 1024
	knnSample   = 10
)

type EncryptInput struct{}

type Encrypted struct {
	Database string `json:"database"`
	Tables   int    `json:"tables"`
	Rows     int64  `json:"rows"`
	KeyID    string `json:"key_id"`
}

type RekeyInput struct {
	NewKey func() ([]byte, error) `json:"-"`
}

type Rekeyed struct {
	Database string `json:"database"`
	From     string `json:"from_key_id"`
	To       string `json:"to_key_id"`
}

func DescribeEncrypt() application.Definition {
	return application.Describe[EncryptInput, Encrypted](EncryptName, application.Command, []application.Authority{application.Bootstrap}, application.LocalUndoable, true)
}

func DescribeRekey() application.Definition {
	return application.Describe[RekeyInput, Rekeyed](RekeyName, application.Command, []application.Authority{application.Bootstrap}, application.LocalUndoable, true)
}

func ParseKey(raw []byte) (db.Key, error) {
	if len(raw) > maxKeyInput {
		return db.Key{}, db.ErrInvalidKey
	}
	return db.ParseKey(strings.TrimSpace(string(raw)))
}

func (s *Service) Encrypt(ctx context.Context, _ EncryptInput) (application.Change[Encrypted], error) {
	key, err := s.key(ctx)
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	if key.IsZero() {
		return application.Change[Encrypted]{}, refuse(errors.New("db encrypt: no database key is configured; set " + db.KeySetting + " or the " + db.KeyCredential + " credential first"))
	}
	cfg, err := s.database()
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	path := cfg.DBPath
	exclusion, err := db.TryServeLock(ctx, path)
	if err != nil {
		return application.Change[Encrypted]{}, fmt.Errorf("db encrypt refused: %w", err)
	}
	defer func() { _ = exclusion.Close() }()
	lock, err := db.Exclusive(ctx, path, db.ShortLockWait)
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	defer func() { _ = lock.Close() }()
	held, err := db.OpenFile(path, db.Key{})
	if errors.Is(err, db.ErrKeyMissing) {
		return application.Change[Encrypted]{}, refuse(fmt.Errorf("db encrypt: %s is already encrypted; the database is unchanged", path))
	}
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	defer func() { _ = held.Close() }()
	if err := db.HoldExclusive(ctx, held); err != nil {
		if errors.Is(err, db.ErrInUse) {
			return application.Change[Encrypted]{}, refuse(fmt.Errorf("db encrypt: %w; the database is unchanged", err))
		}
		return application.Change[Encrypted]{}, err
	}
	if err := db.RefuseFutureSchema(ctx, held); err != nil {
		return application.Change[Encrypted]{}, refuse(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	staged, err := db.Stage(path, info.Mode().Perm())
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	defer staged.Discard()
	if err := chownLike(staged.Path(), info); err != nil {
		return application.Change[Encrypted]{}, err
	}
	if err := db.Export(ctx, held, staged.Path(), key); err != nil {
		return application.Change[Encrypted]{}, err
	}
	want, err := db.TableCounts(ctx, held)
	if err != nil {
		return application.Change[Encrypted]{}, err
	}
	if err := verifyEncrypted(ctx, held, staged.Path(), key, want); err != nil {
		return application.Change[Encrypted]{}, fmt.Errorf("db encrypt: verification of the encrypted copy failed, the database is unchanged: %w", err)
	}
	var rows int64
	for _, n := range want {
		rows += n
	}
	out := Encrypted{Database: path, Tables: len(want), Rows: rows, KeyID: dbkey.ID(key)}
	var auditID int64
	if err := commitStaged(ctx, staged.Path(), key, func(tx *sql.Tx) error {
		var err error
		auditID, err = auditStaged(ctx, tx, state.Entry{Operation: string(EncryptName), Outcome: state.OK, DetailsJSON: fmt.Sprintf(`{"key_id":%q,"tables":%d}`, out.KeyID, out.Tables)})
		return err
	}); err != nil {
		return application.Change[Encrypted]{}, err
	}
	if err := staged.PublishRename(); err != nil {
		return application.Change[Encrypted]{}, err
	}
	return application.Change[Encrypted]{Data: out, AuditID: auditID}, nil
}

func (s *Service) Rekey(ctx context.Context, in RekeyInput) (application.Change[Rekeyed], error) {
	old, err := s.key(ctx)
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	var next db.Key
	if old.IsZero() {
		return application.Change[Rekeyed]{}, refuse(errors.New("db rekey: no database key is configured, so the database is plaintext; rekey only rotates the key of an encrypted database (set " + db.KeySetting + " first)"))
	}
	raw, err := in.NewKey()
	if err == nil {
		next, err = ParseKey(raw)
	}
	if err != nil {
		return application.Change[Rekeyed]{}, fmt.Errorf("db rekey: new key: %w", err)
	}
	logx.AddSecret(hex.EncodeToString(next.Raw()))
	oldID, newID := dbkey.ID(old), dbkey.ID(next)
	if oldID == newID {
		return application.Change[Rekeyed]{}, refuse(errors.New("db rekey: the new key is the current key"))
	}
	cfg, err := s.database()
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	path := cfg.DBPath
	exclusion, err := db.TryServeLock(ctx, path)
	if err != nil {
		return application.Change[Rekeyed]{}, fmt.Errorf("db rekey refused: %w", err)
	}
	defer func() { _ = exclusion.Close() }()
	lock, err := db.Exclusive(ctx, path, db.ShortLockWait)
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	defer func() { _ = lock.Close() }()
	held, err := db.OpenFile(path, old)
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	defer func() { _ = held.Close() }()
	if err := db.HoldExclusive(ctx, held); err != nil {
		if errors.Is(err, db.ErrInUse) {
			return application.Change[Rekeyed]{}, refuse(fmt.Errorf("db rekey: %w; the database is unchanged", err))
		}
		return application.Change[Rekeyed]{}, err
	}
	if err := db.RefuseFutureSchema(ctx, held); err != nil {
		return application.Change[Rekeyed]{}, refuse(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	staged, err := db.Stage(path, info.Mode().Perm())
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	defer staged.Discard()
	if err := chownLike(staged.Path(), info); err != nil {
		return application.Change[Rekeyed]{}, err
	}
	if err := db.Backup(ctx, held, staged.Path(), next); err != nil {
		return application.Change[Rekeyed]{}, err
	}
	want, err := db.TableCounts(ctx, held)
	if err != nil {
		return application.Change[Rekeyed]{}, err
	}
	if err := db.Verify(ctx, staged.Path(), next, want); err != nil {
		return application.Change[Rekeyed]{}, fmt.Errorf("db rekey: verification of the re-encrypted copy failed, the database is unchanged: %w", err)
	}
	var auditID int64
	if err := commitStaged(ctx, staged.Path(), next, func(tx *sql.Tx) error {
		if err := db.RecordRetiredKey(ctx, tx, oldID, newID); err != nil {
			return err
		}
		var err error
		auditID, err = auditStaged(ctx, tx, state.Entry{Operation: string(RekeyName), Outcome: state.OK, DetailsJSON: fmt.Sprintf(`{"from_key_id":%q,"to_key_id":%q}`, oldID, newID)})
		return err
	}); err != nil {
		return application.Change[Rekeyed]{}, err
	}
	if err := staged.PublishRename(); err != nil {
		return application.Change[Rekeyed]{}, err
	}
	return application.Change[Rekeyed]{Data: Rekeyed{Database: path, From: oldID, To: newID}, AuditID: auditID}, nil
}

func refuse(err error) error {
	return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
}

func commitStaged(ctx context.Context, path string, key db.Key, fn func(*sql.Tx) error) (err error) {
	c, err := db.OpenFile(path, key)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func verifyEncrypted(ctx context.Context, src *sql.DB, path string, key db.Key, want map[string]int64) error {
	if err := db.Verify(ctx, path, key, want); err != nil {
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

func chownLike(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Chown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("give the new database file the owner of %s: %w", path, err)
	}
	return nil
}

func auditStaged(ctx context.Context, tx *sql.Tx, e state.Entry) (int64, error) {
	if ok, err := hasAudit(ctx, tx); err != nil || !ok {
		return 0, err
	}
	return state.New(nil).Audit(ctx, tx, e)
}
