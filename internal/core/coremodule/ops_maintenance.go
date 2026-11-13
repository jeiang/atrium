package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
)

func maintenanceOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			maintenanceapp.DescribeCheck(), maintenanceapp.DescribeVerifyArchive(),
			maintenanceapp.DescribeBackup(), maintenanceapp.DescribeDBBackup(), maintenanceapp.DescribeRestore(),
			maintenanceapp.DescribeEncrypt(), maintenanceapp.DescribeRekey(), maintenanceapp.DescribeImportV2(), maintenanceapp.DescribeSQL(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "db check", Uses: []application.Name{maintenanceapp.CheckName}},
			{Kind: application.CLI, Target: "db backup", Uses: []application.Name{maintenanceapp.DBBackupName}},
			{Kind: application.CLI, Target: "db encrypt", Uses: []application.Name{maintenanceapp.EncryptName}},
			{Kind: application.CLI, Target: "db rekey", Uses: []application.Name{maintenanceapp.RekeyName}},
			{Kind: application.CLI, Target: "db import-v2", Uses: []application.Name{maintenanceapp.ImportV2Name}},
			{Kind: application.CLI, Target: "db shell", Uses: []application.Name{maintenanceapp.SQLName}},
			{Kind: application.Job, Target: "core/backup", Uses: []application.Name{maintenanceapp.BackupName}},
			{Kind: application.CLI, Target: "backup", Uses: []application.Name{maintenanceapp.BackupName}},
			{Kind: application.CLI, Target: "restore", Uses: []application.Name{maintenanceapp.VerifyArchiveName, maintenanceapp.RestoreName}},
		}
}
