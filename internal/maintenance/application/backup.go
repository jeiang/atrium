package application

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/passrun"
)

const (
	BackupName   application.Name = "maintenance.backup"
	DBBackupName application.Name = "maintenance.db-backup"
	RestoreName  application.Name = "maintenance.restore"
)

type BackupInput struct {
	Dest   string                                  `json:"dest"`
	Forget func(context.Context, db.Querier) error `json:"-"`
}

type Backup struct {
	Dest       string     `json:"dest"`
	Encryption Encryption `json:"encryption"`
	Files      int        `json:"files"`
}

type RestoreInput struct {
	Archive           string  `json:"archive"`
	KeyFile           KeyFile `json:"key_file"`
	ReplaceUnopenable bool    `json:"replace_unopenable"`
}

type Restored struct {
	Verified   bool       `json:"verified"`
	Archive    Archive    `json:"archive"`
	Database   string     `json:"database"`
	Moved      []string   `json:"moved"`
	Encryption Encryption `json:"encryption"`
	Configured Encryption `json:"configured"`
}

func DescribeBackup() application.Definition {
	return application.Describe[BackupInput, Backup](BackupName, application.Command, []application.Authority{application.Operator, application.System}, application.NoEffect, true)
}

func DescribeDBBackup() application.Definition {
	return application.Describe[BackupInput, Backup](DBBackupName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeRestore() application.Definition {
	return application.Describe[RestoreInput, Restored](RestoreName, application.Command, []application.Authority{application.Bootstrap}, application.LocalUndoable, true)
}

func (s *Service) Backup(ctx context.Context, in BackupInput) (application.Change[Backup], error) {
	cfg, err := s.database()
	if err != nil {
		return application.Change[Backup]{}, err
	}
	key, err := s.key(ctx)
	if err != nil {
		return application.Change[Backup]{}, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(key))
	if err != nil {
		return application.Change[Backup]{}, err
	}
	defer func() { _ = d.Close() }()
	count, err := backup.Write(ctx, d, cfg, key, in.Dest, in.Forget)
	details := map[string]any{"encrypted": !key.IsZero()}
	if err == nil {
		details["files"] = count
	}
	id, err := s.settle(ctx, d, BackupName, backupTarget(in.Dest), details, err)
	if err != nil {
		return application.Change[Backup]{}, err
	}
	return application.Change[Backup]{Data: Backup{Dest: in.Dest, Encryption: encryption(key), Files: count}, AuditID: id}, nil
}

func (s *Service) DBBackup(ctx context.Context, in BackupInput) (application.Change[Backup], error) {
	cfg, err := s.database()
	if err != nil {
		return application.Change[Backup]{}, err
	}
	key, err := s.key(ctx)
	if err != nil {
		return application.Change[Backup]{}, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(key))
	if err != nil {
		return application.Change[Backup]{}, err
	}
	defer func() { _ = d.Close() }()
	if err := db.RequireV3(ctx, d.Read); err != nil {
		return application.Change[Backup]{}, err
	}
	id, err := s.settle(ctx, d, DBBackupName, backupTarget(in.Dest), map[string]any{"encrypted": !key.IsZero()}, copyDatabase(ctx, d, cfg, key, in.Dest))
	if err != nil {
		return application.Change[Backup]{}, err
	}
	return application.Change[Backup]{Data: Backup{Dest: in.Dest, Encryption: encryption(key)}, AuditID: id}, nil
}

func backupTarget(path string) string { return "backup:" + filepath.Base(path) }

func copyDatabase(ctx context.Context, d *db.DB, cfg config.Config, key db.Key, dest string) error {
	if err := backup.RefuseDestination(cfg, dest); err != nil {
		return err
	}
	locks, err := db.ResourceLocks(ctx, cfg.DBPath, []string{passrun.Backup}, db.ShortLockWait)
	if err != nil {
		return err
	}
	defer func() { _ = locks.Close() }()
	if _, err := os.Lstat(dest); err == nil {
		return &backup.Refusal{Err: fmt.Errorf("backup destination %s already exists", dest)}
	}
	staged, err := db.Stage(dest, 0o600)
	if err != nil {
		return err
	}
	defer staged.Discard()
	if err := d.BackupTo(ctx, staged.Path(), key); err != nil {
		return err
	}
	if err := db.Verify(ctx, staged.Path(), key, nil); err != nil {
		return fmt.Errorf("backup verification: %w", err)
	}
	return staged.PublishLink()
}

func (s *Service) Restore(ctx context.Context, in RestoreInput) (application.Change[Restored], error) {
	cfg, err := s.config()
	if err != nil {
		return application.Change[Restored]{}, err
	}
	configured, err := s.key(ctx)
	if err != nil {
		return application.Change[Restored]{}, err
	}
	p, err := backup.Prepare(ctx, cfg, configured, in.Archive, backup.KeyFile(in.KeyFile), false)
	if err != nil {
		return application.Change[Restored]{}, refused(err)
	}
	defer p.Discard()
	out := Restored{Verified: true, Archive: archive(in.Archive, p.Report), Database: cfg.DBPath, Encryption: encryption(p.Key), Configured: encryption(configured)}
	moved, err := p.Install(ctx, cfg, configured, in.ReplaceUnopenable)
	if err != nil {
		return application.Change[Restored]{Data: out}, refused(err)
	}
	out.Moved = moved
	d, err := db.Connect(cfg.DBPath, db.WithKey(p.Key))
	if err != nil {
		return application.Change[Restored]{Data: out}, err
	}
	defer func() { _ = d.Close() }()
	id, err := audit(ctx, d, RestoreName, state.OK, backupTarget(in.Archive), map[string]any{"files": p.Report.Files, "schema": p.Report.To, "encrypted": !p.Key.IsZero()})
	if err != nil {
		return application.Change[Restored]{Data: out}, err
	}
	return application.Change[Restored]{Data: out, AuditID: id}, nil
}
