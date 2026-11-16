package application

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/db"
)

const (
	BackupName  application.Name = "maintenance.backup"
	RestoreName application.Name = "maintenance.restore"
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

func backupTarget(path string) string { return "backup:" + filepath.Base(path) }

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
