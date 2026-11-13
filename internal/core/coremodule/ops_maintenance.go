package coremodule

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/web"
)

type BackupCreated struct {
	Name       string                    `json:"name"`
	Encryption maintenanceapp.Encryption `json:"encryption"`
	Files      int                       `json:"files"`
}

type BackupRef struct {
	Name string `json:"name"`
}

type BackupVerified struct {
	Name       string                    `json:"name"`
	CreatedAt  string                    `json:"created_at"`
	Encryption maintenanceapp.Encryption `json:"encryption"`
	KeyIDs     []string                  `json:"key_ids"`
	From       int                       `json:"from_schema"`
	To         int                       `json:"to_schema"`
	Tables     []maintenanceapp.Table    `json:"tables"`
	KNN        []string                  `json:"knn"`
	Files      int                       `json:"files"`
}

type DatabaseCheck struct {
	Encryption maintenanceapp.Encryption `json:"encryption"`
	Opened     bool                      `json:"opened"`
	Intact     bool                      `json:"intact"`
	Tables     []maintenanceapp.Table    `json:"tables"`
}

func (m Module) maintenanceOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			maintenanceapp.DescribeCheck(), maintenanceapp.DescribeVerifyArchive(),
			maintenanceapp.DescribeBackup(), maintenanceapp.DescribeDBBackup(), maintenanceapp.DescribeRestore(),
			maintenanceapp.DescribeEncrypt(), maintenanceapp.DescribeRekey(), maintenanceapp.DescribeImportV2(), maintenanceapp.DescribeSQL(),
		}, append(m.maintenanceRoutes(nil), []application.Binding{
			{Kind: application.CLI, Target: "db check", Uses: []application.Name{maintenanceapp.CheckName}},
			{Kind: application.CLI, Target: "db backup", Uses: []application.Name{maintenanceapp.DBBackupName}},
			{Kind: application.CLI, Target: "db encrypt", Uses: []application.Name{maintenanceapp.EncryptName}},
			{Kind: application.CLI, Target: "db rekey", Uses: []application.Name{maintenanceapp.RekeyName}},
			{Kind: application.CLI, Target: "db import-v2", Uses: []application.Name{maintenanceapp.ImportV2Name}},
			{Kind: application.CLI, Target: "db shell", Uses: []application.Name{maintenanceapp.SQLName}},
			{Kind: application.Job, Target: "core/backup", Uses: []application.Name{maintenanceapp.BackupName}},
			{Kind: application.CLI, Target: "backup", Uses: []application.Name{maintenanceapp.BackupName}},
			{Kind: application.CLI, Target: "restore", Uses: []application.Name{maintenanceapp.VerifyArchiveName, maintenanceapp.RestoreName}},
		}...)
}

func backupDestination(ctx context.Context, now func() float64, ext string) (string, string, error) {
	dir, err := backup.Directory(ctx)
	if err != nil {
		return "", "", notConfigured(err)
	}
	name := backup.ArchiveName(time.Unix(0, int64(now()*float64(time.Second))), ext)
	return filepath.Join(dir, name), name, nil
}

func backupNamed(ctx context.Context, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", &application.Error{Code: application.Invalid, Message: "name a backup file inside the backup directory",
			Fields: []application.Field{{Name: "name", Code: string(application.Invalid), Message: "name a backup file inside the backup directory"}}}
	}
	dir, err := backup.Directory(ctx)
	if err != nil {
		return "", notConfigured(err)
	}
	return filepath.Join(dir, name), nil
}

func notConfigured(err error) error {
	return &application.Error{Code: application.NotConfigured, Message: err.Error(), Cause: err}
}

func exclusive[T any](ctx context.Context, cfg config.Config, run func() (T, error)) (T, error) {
	locks, err := db.ResourceLocks(ctx, cfg.DBPath, []string{passrun.Backup}, db.ShortLockWait)
	if err != nil {
		var zero T
		return zero, maintenanceapp.Busy(err)
	}
	defer func() { _ = locks.Close() }()
	return run()
}

func (m Module) maintenanceRoutes(d *web.Deps) []application.Binding {
	const area = "settings"
	svc := func() *maintenanceapp.Service { return maintenanceapp.New(d.Env, m.Now) }
	refusable := []application.Code{application.Invalid, application.Refused, application.Conflict, application.NotConfigured}
	created := func(name string, change application.Change[maintenanceapp.Backup], err error) (application.Change[BackupCreated], error) {
		if err != nil {
			return application.Change[BackupCreated]{}, err
		}
		return application.Change[BackupCreated]{Data: BackupCreated{Name: name, Encryption: change.Data.Encryption, Files: change.Data.Files}, AuditID: change.AuditID}, nil
	}
	return []application.Binding{
		api.Command(d, maintenanceapp.DescribeCheck(), application.APIShape{Area: area, Errors: []application.Code{application.Conflict}},
			"POST /maintenance/check", func(ctx context.Context, in maintenanceapp.CheckInput) (application.Change[DatabaseCheck], error) {
				return exclusive(ctx, d.Cfg, func() (application.Change[DatabaseCheck], error) {
					read, err := svc().Check(ctx, in)
					c := read.Data
					return application.Change[DatabaseCheck]{Data: DatabaseCheck{Encryption: c.Encryption, Opened: c.Opened, Intact: c.Intact, Tables: c.Tables}}, err
				})
			}),
		api.Command(d, maintenanceapp.DescribeVerifyArchive(), application.APIShape{Area: area, Errors: refusable},
			"POST /maintenance/backups/verify", func(ctx context.Context, in BackupRef) (application.Change[BackupVerified], error) {
				path, err := backupNamed(ctx, in.Name)
				if err != nil {
					return application.Change[BackupVerified]{}, err
				}
				return exclusive(ctx, d.Cfg, func() (application.Change[BackupVerified], error) {
					read, err := svc().VerifyArchive(ctx, maintenanceapp.VerifyArchiveInput{Archive: path})
					if err != nil {
						return application.Change[BackupVerified]{}, err
					}
					a := read.Data
					return application.Change[BackupVerified]{Data: BackupVerified{Name: in.Name, CreatedAt: a.CreatedAt, Encryption: a.Encryption, KeyIDs: a.KeyIDs,
						From: a.From, To: a.To, Tables: a.Tables, KNN: a.KNN, Files: a.Files}}, nil
				})
			}),
		api.Command(d, maintenanceapp.DescribeBackup(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: refusable},
			"POST /maintenance/backups", func(ctx context.Context, _ struct{}) (application.Change[BackupCreated], error) {
				dest, name, err := backupDestination(ctx, m.Now, ".backup")
				if err != nil {
					return application.Change[BackupCreated]{}, err
				}
				change, err := svc().Backup(ctx, maintenanceapp.BackupInput{Dest: dest})
				return created(name, change, err)
			}),
		api.Command(d, maintenanceapp.DescribeDBBackup(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: refusable},
			"POST /maintenance/db-backups", func(ctx context.Context, _ struct{}) (application.Change[BackupCreated], error) {
				dest, name, err := backupDestination(ctx, m.Now, ".db")
				if err != nil {
					return application.Change[BackupCreated]{}, err
				}
				change, err := svc().DBBackup(ctx, maintenanceapp.BackupInput{Dest: dest})
				return created(name, change, err)
			}),
	}
}
