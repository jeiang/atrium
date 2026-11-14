package dbcmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
)

func BackupCommand(svc *maintenanceapp.Service) *cli.Command {
	return &cli.Command{
		Name: "backup",
		Help: "write one archive holding a consistent copy of the database and the uploaded files; safe while atrium runs",
		Setup: func(a *cli.Args) cli.Handler {
			dest := a.Arg("dest", cli.Opt{Help: "path of the new archive; must not exist"})
			return func(e *cli.Env) int {
				res, err := svc.Backup(e.Ctx, maintenanceapp.BackupInput{Dest: *dest})
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "backup written to %s (%s, %d files)\n", *dest, encryption(res.Data.Encryption), res.Data.Files)
				return cli.ExitOK
			}
		},
	}
}

func RestoreCommand(svc *maintenanceapp.Service) *cli.Command {
	return &cli.Command{
		Name: "restore",
		Help: "replace the database and uploaded files with the contents of a backup archive; stop the atrium units and timers that use the database first",
		Setup: func(a *cli.Args) cli.Handler {
			verify := a.Bool("verify", cli.Opt{Help: "check the archive in a temporary location and change nothing"})
			keyFile := a.String("key-file", "", cli.Opt{Metavar: "PATH", Help: "file holding the 64-hex-digit key the archive was made under, when it differs from the configured key"})
			replaceUnopenable := a.Bool("replace-unopenable", cli.Opt{Help: "replace an existing database that cannot be opened and locked with an available key; it is kept aside"})
			archive := a.Arg("archive", cli.Opt{Help: "path of the backup archive"})
			return func(e *cli.Env) int {
				key, err := readKeyFile(*keyFile)
				if err != nil {
					return cli.Fail(e, err)
				}
				if *verify {
					res, err := svc.VerifyArchive(e.Ctx, maintenanceapp.VerifyArchiveInput{Archive: *archive, KeyFile: key})
					if err != nil {
						return cli.Fail(e, err)
					}
					printArchive(e, res.Data)
					_, _ = fmt.Fprintln(e.Stdout, "verified; nothing was written")
					return cli.ExitOK
				}
				res, err := svc.Restore(e.Ctx, maintenanceapp.RestoreInput{Archive: *archive, KeyFile: key, ReplaceUnopenable: *replaceUnopenable})
				if res.Data.Verified {
					printArchive(e, res.Data.Archive)
				}
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "restored %s\n", res.Data.Database)
				for _, m := range res.Data.Moved {
					_, _ = fmt.Fprintf(e.Stdout, "previous state kept at %s\n", m)
				}
				if got := res.Data.Encryption; got.Encrypted && got.KeyID != res.Data.Configured.KeyID {
					_, _ = fmt.Fprintf(e.Stdout, "the restored database is encrypted under key id %s, not the configured key; configure that key before starting atrium\n", got.KeyID)
				}
				return cli.ExitOK
			}
		},
	}
}

func readKeyFile(path string) (maintenanceapp.KeyFile, error) {
	if path == "" {
		return maintenanceapp.KeyFile{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return maintenanceapp.KeyFile{}, errors.New("--key-file could not be opened")
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyInput+1))
	if err != nil {
		return maintenanceapp.KeyFile{}, err
	}
	return maintenanceapp.KeyFile{Given: true, Raw: raw}, nil
}

func printArchive(e *cli.Env, a maintenanceapp.Archive) {
	w := e.Stdout
	_, _ = fmt.Fprintf(w, "archive: %s\ncreated: %s\n", a.Path, a.CreatedAt)
	if a.From == a.To {
		_, _ = fmt.Fprintf(w, "schema: %04d\n", a.To)
	} else {
		_, _ = fmt.Fprintf(w, "schema: %04d, migrated to %04d\n", a.From, a.To)
	}
	if a.Encryption.Encrypted {
		_, _ = fmt.Fprintf(w, "encryption: encrypted, key id %s (key ids: %s)\n", a.Encryption.KeyID, strings.Join(a.KeyIDs, ", "))
	} else {
		_, _ = fmt.Fprintln(w, "encryption: plaintext")
	}
	_, _ = fmt.Fprintln(w, "integrity_check: ok")
	for _, t := range a.Tables {
		_, _ = fmt.Fprintf(w, "%s %s: %d\n", t.Kind, t.Name, t.Rows)
	}
	for _, name := range a.KNN {
		_, _ = fmt.Fprintf(w, "knn %s: ok\n", name)
	}
	_, _ = fmt.Fprintf(w, "files: %d\n", a.Files)
}

func encryption(e maintenanceapp.Encryption) string {
	if !e.Encrypted {
		return "plaintext"
	}
	return "encrypted, key id " + e.KeyID
}
