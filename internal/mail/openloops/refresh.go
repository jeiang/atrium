package openloops

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/features"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
)

func BackfillHeaders(ctx context.Context, d *db.DB, cfg config.Config, now float64) (int, error) {
	r := mail.Read(ctx, d.Read)
	window := r.Int(mail.KeyLoopsWindowDays)
	limit := r.Int(mail.KeyLoopsBackfillLimit)
	if err := r.Err(); err != nil {
		return 0, err
	}
	rows, err := d.Read.QueryContext(ctx,
		"SELECT id, raw_path FROM messages WHERE in_reply_to IS NULL AND date_ts >= ? ORDER BY id DESC LIMIT ?",
		now-float64(window)*Day, limit)
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
			msg, err := mime.ParseHeaders(data)
			if err != nil {
				return 0, err
			}
			inReplyTo = strings.Join(features.MsgIDList(features.Header(msg, "In-Reply-To")), " ")
			refs = strings.Join(features.MsgIDList(features.Header(msg, "References")), " ")
		}
		if _, err := d.Write.ExecContext(ctx, "UPDATE messages SET in_reply_to = ?, refs = ? WHERE id = ?", inReplyTo, refs, r.id); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

type Options struct {
	Env   config.Env
	Judge Judge
	Clock func() float64
}

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
		{"backfilled", func() (any, error) { return BackfillHeaders(ctx, d, cfg, now) }},
		{"loops", func() (any, error) {
			counts, err := Compute(ctx, d, now)
			return counts.Dict(), err
		}},
		{"commitments", func() (any, error) { return ScanCommitments(ctx, d, env, now, o.Judge) }},
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
