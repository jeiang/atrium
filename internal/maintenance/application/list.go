package application

import (
	"context"
	"errors"
	"io/fs"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/module"
)

const ListBackupsName application.Name = "maintenance.backup.list"

const (
	ListBackupsMax        = 100
	ListBackupsExamineMax = backup.ExamineMax

	LocationReady        = "ready"
	LocationUnconfigured = "unconfigured"
	LocationMissing      = "missing"

	ProblemOldFormat   = "old_format"
	ProblemNewerFormat = "newer_format"
)

type ListBackupsInput struct {
	Limit int `json:"limit"`
}

type BackupList struct {
	Location   BackupLocation `json:"location"`
	Items      []BackupEntry  `json:"items"`
	Total      int            `json:"total"`
	Unexamined int            `json:"unexamined"`
}

type BackupLocation struct {
	State     string `json:"state"`
	HostKey   string `json:"host_key"`
	NixOption string `json:"nix_option"`
}

type BackupEntry struct {
	Name       string        `json:"name"`
	SizeBytes  int64         `json:"size_bytes"`
	ModifiedAt string        `json:"modified_at"`
	Prunable   bool          `json:"prunable"`
	Format     int           `json:"format"`
	Problem    string        `json:"problem,omitempty"`
	Header     *BackupHeader `json:"header"`
}

type BackupHeader struct {
	CreatedAt  string     `json:"created_at"`
	Schema     int        `json:"schema"`
	Encryption Encryption `json:"encryption"`
	Files      int        `json:"files"`
}

func DescribeListBackups() application.Definition {
	return application.Describe[ListBackupsInput, BackupList](ListBackupsName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) ListBackups(ctx context.Context, in ListBackupsInput) (application.Read[BackupList], error) {
	out := BackupList{
		Location: BackupLocation{State: LocationReady, HostKey: module.Core + "." + backup.KeyDirectory, NixOption: "services.atrium.backup.directory"},
		Items:    []BackupEntry{},
	}
	dir, err := backup.Directory(ctx)
	if err != nil {
		out.Location.State = LocationUnconfigured
		return application.Read[BackupList]{Data: out}, nil
	}
	listing, err := backup.List(dir, min(max(in.Limit, 1), ListBackupsMax))
	if errors.Is(err, fs.ErrNotExist) {
		out.Location.State = LocationMissing
		return application.Read[BackupList]{Data: out}, nil
	}
	if err != nil {
		return application.Read[BackupList]{}, err
	}
	out.Total, out.Unexamined = listing.Total, listing.Unexamined
	for _, l := range listing.Archives {
		e := BackupEntry{Name: l.Name, SizeBytes: l.Size, ModifiedAt: l.Modified.UTC().Format(time.RFC3339), Prunable: l.Prunable, Format: l.Header.Format}
		switch {
		case l.Problem == nil:
			h := &BackupHeader{CreatedAt: l.Header.CreatedAt, Schema: l.Header.Schema, Files: l.Header.Files}
			if l.Header.Seal != nil {
				h.Encryption = Encryption{Encrypted: true, KeyID: l.Header.Seal.KeyID}
			}
			e.Header = h
		case errors.Is(l.Problem, backup.ErrOldFormat):
			e.Problem = ProblemOldFormat
		default:
			e.Problem = ProblemNewerFormat
		}
		out.Items = append(out.Items, e)
	}
	return application.Read[BackupList]{Data: out}, nil
}
