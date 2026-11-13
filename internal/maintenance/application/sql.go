package application

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
)

const SQLName application.Name = "maintenance.sql"

type SQLInput struct {
	In          io.Reader `json:"-"`
	Out         io.Writer `json:"-"`
	Errors      io.Writer `json:"-"`
	Interactive bool      `json:"interactive"`
}

type SQLResult struct {
	Statements int `json:"statements"`
	Failed     int `json:"failed"`
}

func DescribeSQL() application.Definition {
	return application.Describe[SQLInput, SQLResult](SQLName, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}

func (s *Service) SQL(ctx context.Context, in SQLInput) (application.Change[SQLResult], error) {
	cfg, err := s.database()
	if err != nil {
		return application.Change[SQLResult]{}, err
	}
	key, err := s.key(ctx)
	if err != nil {
		return application.Change[SQLResult]{}, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(key))
	if err != nil {
		return application.Change[SQLResult]{}, err
	}
	defer func() { _ = d.Close() }()
	if err := db.RequireV3(ctx, d.Read); err != nil {
		return application.Change[SQLResult]{}, err
	}
	conn, err := d.Write.Conn(ctx)
	if err != nil {
		return application.Change[SQLResult]{}, err
	}
	sh := &shell{ctx: ctx, conn: conn, in: in}
	runErr := sh.loop()
	out := SQLResult{Statements: sh.statements, Failed: sh.failed}
	if err := errors.Join(rollbackOpen(ctx, conn), conn.Close()); err != nil {
		return application.Change[SQLResult]{Data: out}, errors.Join(runErr, err)
	}
	ok, err := hasAudit(ctx, d.Write)
	if err != nil || !ok {
		return application.Change[SQLResult]{Data: out}, errors.Join(runErr, err)
	}
	outcome := state.OK
	if runErr != nil || out.Failed > 0 {
		outcome = state.Failed
	}
	auditID, err := state.New(d).Audit(ctx, nil, state.Entry{Operation: string(SQLName), Outcome: outcome, DetailsJSON: fmt.Sprintf(`{"statements":%d,"failed":%d}`, out.Statements, out.Failed)})
	return application.Change[SQLResult]{Data: out, AuditID: auditID}, errors.Join(runErr, err)
}

func rollbackOpen(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil && !strings.Contains(err.Error(), "no transaction is active") {
		return err
	}
	return nil
}

type shell struct {
	ctx        context.Context
	conn       *sql.Conn
	in         SQLInput
	statements int
	failed     int
}

func (sh *shell) loop() error {
	in := bufio.NewReader(sh.in.In)
	pending := ""
	for {
		if sh.in.Interactive {
			prompt := "atrium> "
			if strings.TrimSpace(pending) != "" {
				prompt = "   ...> "
			}
			_, _ = fmt.Fprint(sh.in.Out, prompt)
		}
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		eof := errors.Is(err, io.EOF)
		if strings.TrimSpace(pending) == "" && strings.HasPrefix(strings.TrimSpace(line), ".") {
			if sh.meta(strings.TrimSpace(line)) {
				break
			}
		} else {
			pending += line
			stmts, rest := splitStatements(pending)
			pending = rest
			for _, stmt := range stmts {
				sh.exec(stmt)
			}
		}
		if eof {
			if strings.TrimSpace(pending) != "" {
				sh.exec(pending)
			}
			break
		}
	}
	return nil
}

func (sh *shell) exec(stmt string) {
	sh.statements++
	if err := sh.run(stmt); err != nil {
		sh.failed++
		_, _ = fmt.Fprintf(sh.in.Errors, "error: %v\n", err)
	}
}

func (sh *shell) meta(line string) (done bool) {
	switch line {
	case ".quit", ".exit":
		return true
	case ".tables":
		if err := sh.run("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"); err != nil {
			sh.failed++
			_, _ = fmt.Fprintf(sh.in.Errors, "error: %v\n", err)
		}
		return false
	}
	sh.failed++
	_, _ = fmt.Fprintf(sh.in.Errors, "error: unknown command %q; known commands: .tables .quit .exit\n", line)
	return false
}

func (sh *shell) run(stmt string) error {
	rows, err := sh.conn.QueryContext(sh.ctx, stmt)
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
		_, _ = fmt.Fprintln(sh.in.Out, strings.Join(cells, "\t"))
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
		case c == '/' && i+1 < len(text) && text[i+1] == '*':
			j := strings.Index(text[i+2:], "*/")
			if j < 0 {
				return stmts, text[start:]
			}
			i += j + 3
		case c == ';':
			stmt := strings.TrimSpace(text[start : i+1])
			if triggerDepth(stmt) > 0 {
				continue
			}
			if hasSQL(stmt) {
				stmts = append(stmts, stmt)
			}
			start = i + 1
		}
	}
	return stmts, text[start:]
}

func isTrigger(stmt string) bool {
	words := strings.Fields(strings.ToUpper(stmt))
	if len(words) < 2 || words[0] != "CREATE" {
		return false
	}
	if words[1] == "TEMP" || words[1] == "TEMPORARY" {
		words = words[1:]
	}
	return len(words) > 1 && words[1] == "TRIGGER"
}

func triggerDepth(stmt string) int {
	if !isTrigger(stmt) {
		return 0
	}
	depth := 0
	for _, word := range strings.Fields(stripQuoted(stmt)) {
		switch strings.ToUpper(strings.Trim(word, ";(),")) {
		case "BEGIN", "CASE":
			depth++
		case "END":
			depth--
		}
	}
	return depth
}

func stripQuoted(text string) string {
	var b strings.Builder
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			b.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func hasSQL(stmt string) bool {
	text := stmt
	for {
		text = strings.TrimSpace(text)
		switch {
		case strings.HasPrefix(text, "--"):
			if i := strings.IndexByte(text, '\n'); i >= 0 {
				text = text[i+1:]
				continue
			}
			return false
		case strings.HasPrefix(text, "/*"):
			if i := strings.Index(text[2:], "*/"); i >= 0 {
				text = text[i+4:]
				continue
			}
			return false
		case text == "" || text == ";":
			return false
		default:
			return true
		}
	}
}
