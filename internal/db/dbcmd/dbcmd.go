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
			{Name: "shell", Help: "run SQL statements from standard input against the database", Setup: s.shell},
			{Name: "check", Help: "verify database integrity and report FTS5 and vec0 table counts", Setup: s.check},
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

func (s *service) check(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		res, err := s.maintenance.Check(e.Ctx, maintenanceapp.CheckInput{})
		c := res.Data
		if c.Opened {
			_, _ = fmt.Fprintf(e.Stdout, "database: %s\nencryption: %s\n", c.Database, encryption(c.Encryption))
		}
		switch {
		case c.Intact:
			_, _ = fmt.Fprintf(e.Stdout, "integrity_check: ok\n")
		case c.Opened:
			_, _ = fmt.Fprintf(e.Stdout, "integrity_check: FAILED\n")
		}
		if err != nil {
			return cli.Fail(e, err)
		}
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
