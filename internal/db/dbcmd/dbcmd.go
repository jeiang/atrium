package dbcmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jeiang/atrium/internal/cli"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
)

const maxKeyInput = 1024

func Command(maintenance *maintenanceapp.Service) *cli.Command {
	s := &service{maintenance: maintenance}
	return &cli.Command{
		Name: "db",
		Commands: []*cli.Command{
			{Name: "encrypt", Help: "encrypt a plaintext database with the configured key into a fresh file, verify it and replace the original; stop the atrium units and timers that use the database first", Setup: s.encrypt},
			{Name: "rekey", Help: "re-encrypt the database under a new key read from a file or standard input; stop the atrium units and timers that use the database first", Setup: s.rekey},
			{Name: "backup", Help: "write a consistent copy of the database, encrypted like the source", Setup: s.backup},
			{Name: "shell", Help: "run SQL statements from standard input against the database", Setup: s.shell},
			{Name: "check", Help: "verify database integrity and report FTS5 and vec0 table counts", Setup: s.check},
			{Name: "import-v2", Help: "copy a v2 database into this fresh v3 database", Setup: s.importV2},
		},
	}
}

type service struct {
	maintenance *maintenanceapp.Service
}

func readKey(path string, stdin io.Reader) func() ([]byte, error) {
	return func() ([]byte, error) {
		r := stdin
		if path != "" {
			f, err := os.Open(path)
			if err != nil {
				return nil, errors.New("--new-key-file could not be opened")
			}
			defer func() { _ = f.Close() }()
			r = f
		}
		return io.ReadAll(io.LimitReader(r, maxKeyInput+1))
	}
}

func (s *service) backup(a *cli.Args) cli.Handler {
	dest := a.Arg("dest", cli.Opt{Help: "path of the new backup file; must not exist"})
	return func(e *cli.Env) int {
		res, err := s.maintenance.DBBackup(e.Ctx, maintenanceapp.BackupInput{Dest: *dest})
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "backup written to %s (%s)\n", *dest, encryption(res.Data.Encryption))
		return cli.ExitOK
	}
}

func (s *service) check(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		res, err := s.maintenance.Check(e.Ctx, maintenanceapp.CheckInput{})
		c := res.Data
		if c.Opened {
			_, _ = fmt.Fprintf(e.Stdout, "database: %s\nencryption: %s\n", c.Database, encryption(c.Encryption))
		}
		if c.Opened && !c.Intact {
			_, _ = fmt.Fprintf(e.Stdout, "integrity_check: FAILED\n")
		}
		if err != nil {
			return cli.Fail(e, err)
		}
		_, _ = fmt.Fprintf(e.Stdout, "integrity_check: ok\n")
		for _, t := range c.Tables {
			_, _ = fmt.Fprintf(e.Stdout, "%s %s: %d\n", t.Kind, t.Name, t.Rows)
		}
		return cli.ExitOK
	}
}

func (s *service) encrypt(a *cli.Args) cli.Handler {
	a.RejectExtras("db encrypt takes no arguments; the key comes from the " + maintenanceapp.KeySetting + " setting")
	return func(e *cli.Env) int {
		res, err := s.maintenance.Encrypt(e.Ctx, maintenanceapp.EncryptInput{})
		if err != nil {
			return cli.Fail(e, err)
		}
		r := res.Data
		_, _ = fmt.Fprintf(e.Stdout, "encrypted %s: %d tables, %d rows, key id %s\n", r.Database, r.Tables, r.Rows, r.KeyID)
		return cli.ExitOK
	}
}

func (s *service) rekey(a *cli.Args) cli.Handler {
	keyFile := a.String("new-key-file", "", cli.Opt{Metavar: "PATH", Help: "file holding the new 64-hex-digit key; standard input is read when omitted"})
	a.RejectExtras("the new key must come from --new-key-file or standard input, not the command line")
	return func(e *cli.Env) int {
		res, err := s.maintenance.Rekey(e.Ctx, maintenanceapp.RekeyInput{NewKey: readKey(*keyFile, e.Stdin)})
		if err != nil {
			return cli.Fail(e, err)
		}
		r := res.Data
		_, _ = fmt.Fprintf(e.Stdout, "rekeyed %s: key id %s -> %s\n", r.Database, r.From, r.To)
		return cli.ExitOK
	}
}

func (s *service) importV2(a *cli.Args) cli.Handler {
	source := a.Arg("path", cli.Opt{Help: "path to the v2 database to import into this fresh v3 database"})
	sourceConfig := a.String("source-config", "", cli.Opt{Metavar: "PATH", Help: "v2 configuration file holding pinned settings, as the v2 ATRIUM_CONFIG file did"})
	sourceEnv := a.String("source-env", "", cli.Opt{Metavar: "PATH", Help: "file of v2 ATRIUM_* environment pins, one NAME=VALUE per line"})
	return func(e *cli.Env) int {
		res, err := s.maintenance.ImportV2(e.Ctx, maintenanceapp.ImportV2Input{Source: *source, SourceConfig: *sourceConfig, SourceEnv: *sourceEnv})
		if err != nil {
			return cli.Fail(e, err)
		}
		for _, line := range res.Data.Lines {
			_, _ = fmt.Fprintln(e.Stdout, line)
		}
		return cli.ExitOK
	}
}
