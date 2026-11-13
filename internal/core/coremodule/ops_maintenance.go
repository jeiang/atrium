package coremodule

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
	"github.com/jeiang/atrium/internal/web"
)

type BackupRequest struct {
	Dest string `json:"dest"`
}

type ArchiveRequest struct {
	Archive string `query:"archive" json:"-"`
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

func (m Module) maintenanceRoutes(d *web.Deps) []application.Binding {
	const area = "settings"
	svc := func() *maintenanceapp.Service { return maintenanceapp.New(d.Env, m.Now) }
	refusable := []application.Code{application.Invalid, application.Refused, application.Conflict}
	return []application.Binding{
		api.Query(d, maintenanceapp.DescribeCheck(), application.APIShape{Area: area},
			"GET /maintenance/check", func(ctx context.Context, in maintenanceapp.CheckInput) (application.Read[maintenanceapp.Check], error) {
				return svc().Check(ctx, in)
			}),
		api.Query(d, maintenanceapp.DescribeVerifyArchive(), application.APIShape{Area: area, Errors: refusable},
			"GET /maintenance/backups/verify", func(ctx context.Context, in ArchiveRequest) (application.Read[maintenanceapp.Archive], error) {
				return svc().VerifyArchive(ctx, maintenanceapp.VerifyArchiveInput{Archive: in.Archive})
			}),
		api.Command(d, maintenanceapp.DescribeBackup(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: refusable},
			"POST /maintenance/backups", func(ctx context.Context, in BackupRequest) (application.Change[maintenanceapp.Backup], error) {
				return svc().Backup(ctx, maintenanceapp.BackupInput{Dest: in.Dest})
			}),
		api.Command(d, maintenanceapp.DescribeDBBackup(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: refusable},
			"POST /maintenance/db-backups", func(ctx context.Context, in BackupRequest) (application.Change[maintenanceapp.Backup], error) {
				return svc().DBBackup(ctx, maintenanceapp.BackupInput{Dest: in.Dest})
			}),
	}
}
