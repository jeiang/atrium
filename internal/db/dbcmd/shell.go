package dbcmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/db"
)

func (s *service) shell(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		d, _, code := s.open(e)
		if code != cli.ExitOK {
			return code
		}
		defer func() { _ = d.Close() }()
		interactive := isTerminal(e.Stdin)
		in := bufio.NewReader(e.Stdin)
		failed := false
		pending := ""
		for {
			if interactive {
				prompt := "atrium> "
				if strings.TrimSpace(pending) != "" {
					prompt = "   ...> "
				}
				_, _ = fmt.Fprint(e.Stdout, prompt)
			}
			line, err := in.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return cli.Fail(e, err)
			}
			eof := errors.Is(err, io.EOF)
			if strings.TrimSpace(pending) == "" && strings.HasPrefix(strings.TrimSpace(line), ".") {
				done, ok := metaCommand(e, d, strings.TrimSpace(line))
				failed = failed || !ok
				if done {
					break
				}
			} else {
				pending += line
				stmts, rest := splitStatements(pending)
				pending = rest
				for _, stmt := range stmts {
					if err := run(e, d, stmt); err != nil {
						failed = true
						_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
					}
				}
			}
			if eof {
				if strings.TrimSpace(pending) != "" {
					if err := run(e, d, pending); err != nil {
						failed = true
						_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
					}
				}
				break
			}
		}
		if failed && !interactive {
			return cli.ExitError
		}
		return cli.ExitOK
	}
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func metaCommand(e *cli.Env, d *db.DB, line string) (done, ok bool) {
	switch line {
	case ".quit", ".exit":
		return true, true
	case ".tables":
		if err := run(e, d, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); err != nil {
			_, _ = fmt.Fprintf(e.Stderr, "error: %v\n", err)
			return false, false
		}
		return false, true
	}
	_, _ = fmt.Fprintf(e.Stderr, "error: unknown command %q; known commands: .tables .quit .exit\n", line)
	return false, false
}

func run(e *cli.Env, d *db.DB, stmt string) error {
	rows, err := d.Write.QueryContext(e.Ctx, stmt)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	cells := make([]string, len(cols))
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for i, v := range vals {
			cells[i] = format(v)
		}
		_, _ = fmt.Fprintln(e.Stdout, strings.Join(cells, "\t"))
	}
	return rows.Err()
}

func format(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		if utf8.Valid(x) {
			return string(x)
		}
		return fmt.Sprintf("x'%x'", x)
	}
	return fmt.Sprint(v)
}

func splitStatements(text string) ([]string, string) {
	var stmts []string
	start := 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"' || c == '`':
			quote = c
		case c == '[':
			quote = ']'
		case c == '-' && i+1 < len(text) && text[i+1] == '-':
			j := strings.IndexByte(text[i:], '\n')
			if j < 0 {
				return stmts, text[start:]
			}
			i += j
		case c == ';':
			if stmt := strings.TrimSpace(text[start : i+1]); stmt != ";" {
				stmts = append(stmts, stmt)
			}
			start = i + 1
		}
	}
	return stmts, text[start:]
}
