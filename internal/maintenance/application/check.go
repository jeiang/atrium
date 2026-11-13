package application

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/backup"
	"github.com/jeiang/atrium/internal/db"
)

const (
	CheckName         application.Name = "maintenance.check"
	VerifyArchiveName application.Name = "maintenance.verify-archive"
)

type CheckInput struct{}

type Check struct {
	Database   string     `json:"database"`
	Encryption Encryption `json:"encryption"`
	Opened     bool       `json:"opened"`
	Intact     bool       `json:"intact"`
	Tables     []Table    `json:"tables"`
}

type KeyFile struct {
	Given bool   `json:"given"`
	Raw   []byte `json:"-"`
}

type VerifyArchiveInput struct {
	Archive string  `json:"archive"`
	KeyFile KeyFile `json:"key_file"`
}

type Archive struct {
	Path       string     `json:"path"`
	CreatedAt  string     `json:"created_at"`
	Encryption Encryption `json:"encryption"`
	KeyIDs     []string   `json:"key_ids"`
	From       int        `json:"from_schema"`
	To         int        `json:"to_schema"`
	Tables     []Table    `json:"tables"`
	KNN        []string   `json:"knn"`
	Files      int        `json:"files"`
}

func DescribeCheck() application.Definition {
	return application.Describe[CheckInput, Check](CheckName, application.Command, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeVerifyArchive() application.Definition {
	return application.Describe[VerifyArchiveInput, Archive](VerifyArchiveName, application.Command, []application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) Check(ctx context.Context, _ CheckInput) (application.Read[Check], error) {
	cfg, err := s.database()
	if err != nil {
		return application.Read[Check]{}, err
	}
	key, err := s.key(ctx)
	if err != nil {
		return application.Read[Check]{}, err
	}
	c, err := db.OpenFile(cfg.DBPath, key)
	if err != nil {
		return application.Read[Check]{}, err
	}
	defer func() { _ = c.Close() }()
	out := Check{Database: cfg.DBPath, Encryption: encryption(key), Opened: true}
	if err := db.IntegrityCheck(ctx, c); err != nil {
		return application.Read[Check]{Data: out}, err
	}
	out.Intact = true
	found, err := db.VirtualTables(ctx, c)
	if err != nil {
		return application.Read[Check]{Data: out}, err
	}
	out.Tables = tables(found)
	return application.Read[Check]{Data: out}, nil
}

func (s *Service) VerifyArchive(ctx context.Context, in VerifyArchiveInput) (application.Read[Archive], error) {
	cfg, err := s.config()
	if err != nil {
		return application.Read[Archive]{}, err
	}
	configured, err := s.key(ctx)
	if err != nil {
		return application.Read[Archive]{}, err
	}
	p, err := backup.Prepare(ctx, cfg, configured, in.Archive, backup.KeyFile(in.KeyFile), true)
	if err != nil {
		return application.Read[Archive]{}, refused(err)
	}
	defer p.Discard()
	return application.Read[Archive]{Data: archive(in.Archive, p.Report)}, nil
}

func Busy(err error) error {
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Conflict, Message: "A backup or database check is already running.", Cause: err}
	}
	return err
}

func refused(err error) error {
	if errors.As(err, new(*db.LockBusy)) {
		return Busy(err)
	}
	var refusal *backup.Refusal
	if errors.As(err, &refusal) || errors.Is(err, backup.ErrNewerSchema) || errors.Is(err, backup.ErrArchiveKey) || errors.Is(err, backup.ErrPlaintextKeyed) {
		return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
	}
	return err
}

func archive(path string, rep backup.Report) Archive {
	out := Archive{Path: path, CreatedAt: rep.Header.CreatedAt, From: rep.From, To: rep.To, Tables: tables(rep.Tables), KNN: rep.KNN, Files: rep.Files}
	if rep.Header.Seal != nil {
		out.Encryption = Encryption{Encrypted: true, KeyID: rep.Header.Seal.KeyID}
		out.KeyIDs = rep.Header.Seal.KeyIDs
	}
	return out
}
