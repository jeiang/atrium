package openloops

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
)

// headerBlock is the bytes up to the first blank line: what a headers-only parse reads.
func headerBlock(data []byte) []byte {
	for start := 0; start < len(data); {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			break
		}
		line := bytes.TrimSuffix(data[start:start+end], []byte("\r"))
		if len(line) == 0 {
			return data[:start+end+1]
		}
		start += end + 1
	}
	return data
}

// BackfillHeaders is backfill_headers: in_reply_to and refs for rows indexed before those columns existed, from the
// stored raw header. A raw file whose headers do not parse leaves both empty.
func BackfillHeaders(ctx context.Context, d *db.DB, cfg config.Config, now float64, limit int) (int, error) {
	rows, err := d.Read.QueryContext(ctx,
		"SELECT id, raw_path FROM messages WHERE in_reply_to IS NULL AND date_ts >= ? ORDER BY id DESC LIMIT ?",
		now-WindowDays*Day, limit)
	if err != nil {
		return 0, err
	}
	type pending struct {
		id   int64
		path sql.NullString
	}
	todo, err := db.Collect(rows, func(r *sql.Rows) (pending, error) {
		var p pending
		err := r.Scan(&p.id, &p.path)
		return p, err
	})
	if err != nil {
		return 0, err
	}
	for _, r := range todo {
		inReplyTo, refs := "", ""
		if path, ok := message.RawFile(cfg.CacheDir, r.path.String); ok {
			data, err := os.ReadFile(path)
			if err != nil {
				return 0, err
			}
			if msg, err := mime.Parse(headerBlock(data)); err == nil {
				inReplyTo = strings.Join(features.MsgIDList(features.Header(msg, "In-Reply-To")), " ")
				refs = strings.Join(features.MsgIDList(features.Header(msg, "References")), " ")
			}
		}
		if _, err := d.Write.ExecContext(ctx, "UPDATE messages SET in_reply_to = ?, refs = ? WHERE id = ?", inReplyTo, refs, r.id); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

// Options are the seams of Refresh: Env is where ATRIUM_TZ is read (config.OS when nil), Judge the commitment
// model (JudgeCommitments when nil) and Clock the db.now of the proposals it creates (the wall clock when nil).
type Options struct {
	Env   config.Env
	Judge Judge
	Clock func() float64
}

// Refresh is refresh: one pass after indexing, thread state, commitments and reminder proposals, each step
// isolated; a step that fails is logged and reports nil.
func Refresh(ctx context.Context, d *db.DB, cfg config.Config, now float64, o Options) py.Dict {
	env := o.Env
	if env == nil {
		env = config.OS
	}
	reminders := &Reminders{DB: d, Now: o.Clock, Env: env}
	steps := []struct {
		name string
		run  func() (any, error)
	}{
		{"backfilled", func() (any, error) { return BackfillHeaders(ctx, d, cfg, now, BackfillLimit) }},
		{"loops", func() (any, error) {
			counts, err := Compute(ctx, d, now)
			return counts.Dict(), err
		}},
		{"commitments", func() (any, error) { return ScanCommitments(ctx, d, cfg, env, now, o.Judge, CommitmentScanLimit) }},
		{"reminders", func() (any, error) { return reminders.Propose(ctx, now, config.TZName(env)) }},
	}
	out := make(py.Dict, 0, len(steps))
	for _, step := range steps {
		result, err := step.run()
		if err != nil {
			log.Error(fmt.Sprintf("open loops: %s failed", step.name), logx.Err, err.Error())
			result = nil
		}
		out = append(out, py.Pair{Key: step.name, Value: result})
	}
	return out
}
