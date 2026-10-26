package media

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	DownloadsPage = "/media/downloads"
	Import        = "import"

	lastCheck  = "last_check"
	lastErrors = "last_errors"
)

var log = logx.Logger("atrium.media")

type ChannelError string

func (e ChannelError) Error() string { return string(e) }

type Channel struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	IMDB          string  `json:"imdb"`
	TMDBID        *int64  `json:"tmdb_id"`
	Pattern       string  `json:"pattern"`
	CaseSensitive bool    `json:"case_sensitive"`
	Primed        bool    `json:"primed"`
	Created       float64 `json:"created"`
}

type Status struct {
	LastCheck any      `json:"last_check"`
	Errors    []string `json:"errors"`
}

type Added struct {
	Channel   string
	Title     string
	Hash      string
	SizeBytes int64
}

type PollResult struct {
	Added  []Added
	Errors []string
}

type PollOptions struct {
	Now   func() float64
	Only  *int64
	Force bool
}

func Now() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func (t Tuning) CompilePattern(pattern string, caseSensitive bool) (*rx.Regexp, error) {
	pattern = py.Strip(pattern)
	if pattern == "" {
		return nil, ChannelError("pattern is empty")
	}
	if utf8.RuneCountInString(pattern) > t.PatternChars {
		return nil, ChannelError(fmt.Sprintf("pattern is longer than %d characters", t.PatternChars))
	}
	var flags rx.Flags
	if !caseSensitive {
		flags = rx.IgnoreCase
	}
	re, err := rx.Compile(pattern, flags)
	if err != nil {
		return nil, ChannelError(fmt.Sprintf("pattern %s: %v", py.Repr(pattern), err))
	}
	return re, nil
}

const channelColumns = "id, name, imdb, tmdb_id, pattern, case_sensitive, primed, created"

func scanChannel(row interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	var tmdb sql.NullInt64
	var caseSensitive, primed int64
	if err := row.Scan(&c.ID, &c.Name, &c.IMDB, &tmdb, &c.Pattern, &caseSensitive, &primed, &c.Created); err != nil {
		return Channel{}, err
	}
	if tmdb.Valid {
		c.TMDBID = &tmdb.Int64
	}
	c.CaseSensitive, c.Primed = caseSensitive != 0, primed != 0
	return c, nil
}

func ListChannels(ctx context.Context, q db.Querier) ([]Channel, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+channelColumns+" FROM media_channels ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Channel, error) { return scanChannel(r) })
}

func GetChannel(ctx context.Context, q db.Querier, id int64) (*Channel, error) {
	c, err := scanChannel(q.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM media_channels WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (t Tuning) AddChannel(ctx context.Context, q db.Querier, name, imdb, pattern string, caseSensitive bool, tmdbID *int64, now func() float64, primed bool) (*Channel, error) {
	name = strings.Join(py.Split(name), " ")
	if runes := []rune(name); len(runes) > t.NameChars {
		name = string(runes[:t.NameChars])
	}
	digits, ok := IMDBDigits(imdb)
	if name == "" {
		return nil, ChannelError("name is empty")
	}
	if !ok {
		return nil, ChannelError(fmt.Sprintf("IMDb ID %s is not tt followed by 7 to 9 digits", py.Repr(imdb)))
	}
	if _, err := t.CompilePattern(pattern, caseSensitive); err != nil {
		return nil, err
	}
	if now == nil {
		now = Now
	}
	var tmdb any
	if tmdbID != nil {
		tmdb = *tmdbID
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO media_channels (name, imdb, tmdb_id, pattern, case_sensitive, primed, created) VALUES (?, ?, ?, ?, ?, ?, ?)",
		name, digits, tmdb, py.Strip(pattern), flag(caseSensitive), flag(primed), now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetChannel(ctx, q, id)
}

func DeleteChannel(ctx context.Context, q db.Querier, id int64) (Channel, error) {
	row, err := GetChannel(ctx, q, id)
	if err != nil {
		return Channel{}, err
	}
	if row == nil {
		return Channel{}, ChannelError(fmt.Sprintf("channel %d does not exist", id))
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM media_channels WHERE id = ?", id); err != nil {
		return Channel{}, err
	}
	return *row, nil
}

func RestoreChannel(ctx context.Context, q db.Querier, row Channel) (*Channel, error) {
	existing, err := GetChannel(ctx, q, row.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ChannelError(fmt.Sprintf("channel %d exists again", row.ID))
	}
	var tmdb any
	if row.TMDBID != nil {
		tmdb = *row.TMDBID
	}
	if _, err := q.ExecContext(ctx,
		"INSERT INTO media_channels (id, name, imdb, tmdb_id, pattern, case_sensitive, primed, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		row.ID, row.Name, row.IMDB, tmdb, row.Pattern, flag(row.CaseSensitive), flag(row.Primed), row.Created); err != nil {
		return nil, err
	}
	return GetChannel(ctx, q, row.ID)
}

type PreviewRow struct {
	Release
	Match bool
}

func (t Tuning) Preview(releases []Release, pattern string, caseSensitive bool) ([]PreviewRow, error) {
	re, err := t.CompilePattern(pattern, caseSensitive)
	if err != nil {
		return nil, err
	}
	out := make([]PreviewRow, 0, len(releases))
	for _, r := range releases {
		m, err := re.Search(r.Title)
		if err != nil {
			return nil, err
		}
		out = append(out, PreviewRow{Release: r, Match: m != nil})
	}
	return out, nil
}

func isSeen(ctx context.Context, q db.Querier, hash string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM media_seen WHERE hash = ?", hash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func markSeen(ctx context.Context, q db.Querier, hash string, ts float64) error {
	_, err := q.ExecContext(ctx, "INSERT OR REPLACE INTO media_seen (hash, ts) VALUES (?, ?)", hash, ts)
	return err
}

func (t Tuning) expireSeen(ctx context.Context, q db.Querier, now float64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM media_seen WHERE ts < ?", now-t.SeenTTL)
	return err
}

func setState(ctx context.Context, q db.Querier, key string, value any) error {
	_, err := q.ExecContext(ctx, "INSERT OR REPLACE INTO media_state (key, value) VALUES (?, ?)", key, py.JSONDumps(value))
	return err
}

func ChannelStatus(ctx context.Context, q db.Querier) (Status, error) {
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM media_state")
	if err != nil {
		return Status{}, err
	}
	state, err := db.Collect(rows, func(r *sql.Rows) ([2]string, error) {
		var kv [2]string
		return kv, r.Scan(&kv[0], &kv[1])
	})
	if err != nil {
		return Status{}, err
	}
	out := Status{Errors: []string{}}
	for _, kv := range state {
		v, err := py.JSONLoads([]byte(kv[1]))
		if err != nil {
			return Status{}, err
		}
		switch kv[0] {
		case lastCheck:
			out.LastCheck = v
		case lastErrors:
			items, _ := v.([]any)
			for _, item := range items {
				text, _ := item.(string)
				out.Errors = append(out.Errors, text)
			}
		}
	}
	return out, nil
}

func gb(size int64) string {
	if size <= 0 {
		return ""
	}
	return " (" + py.FormatFixed(float64(size)/1e9, 1) + " GB)"
}

func (r PollResult) Dict() py.Dict {
	added := make([]any, len(r.Added))
	for i, a := range r.Added {
		added[i] = py.Dict{{Key: "channel", Value: a.Channel}, {Key: "title", Value: a.Title}, {Key: "hash", Value: a.Hash}, {Key: "size_bytes", Value: a.SizeBytes}}
	}
	errs := make([]any, len(r.Errors))
	for i, e := range r.Errors {
		errs[i] = e
	}
	return py.Dict{{Key: "added", Value: added}, {Key: "errors", Value: errs}}
}

func Poll(ctx context.Context, q db.Querier, env config.Env, qb *Qbit, settings Settings, opts PollOptions) (PollResult, error) {
	now := opts.Now
	if now == nil {
		now = Now
	}
	stamp := now()
	result := PollResult{Added: []Added{}, Errors: []string{}}
	channels, err := ListChannels(ctx, q)
	if err != nil {
		return result, err
	}
	for _, rule := range channels {
		if opts.Only != nil && rule.ID != *opts.Only {
			continue
		}
		regex, err := settings.CompilePattern(rule.Pattern, rule.CaseSensitive)
		var releases []Release
		if err == nil {
			releases, err = settings.FetchReleases(ctx, rule.IMDB)
		}
		if err != nil {
			var channelErr ChannelError
			var eztvErr EztvError
			if !errors.As(err, &channelErr) && !errors.As(err, &eztvErr) {
				return result, err
			}
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rule.Name, err))
			continue
		}
		for _, t := range releases {
			m, err := regex.Search(t.Title)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", rule.Name, err))
				continue
			}
			if m == nil {
				continue
			}
			seen, err := isSeen(ctx, q, t.Hash)
			if err != nil {
				return result, err
			}
			if seen && !opts.Force {
				continue
			}
			if rule.Primed || opts.Force {
				if err := qb.AddMagnet(ctx, t.MagnetURL, TagMedia); err != nil {
					var qbitErr QbitError
					if !errors.As(err, &qbitErr) {
						return result, err
					}
					result.Errors = append(result.Errors, fmt.Sprintf("%s: %s: %v", rule.Name, t.Title, err))
					continue
				}
				result.Added = append(result.Added, Added{Channel: rule.Name, Title: t.Title, Hash: t.Hash, SizeBytes: t.SizeBytes})
			}
			if err := markSeen(ctx, q, t.Hash, stamp); err != nil {
				return result, err
			}
		}
		if _, err := q.ExecContext(ctx, "UPDATE media_channels SET primed = 1 WHERE id = ?", rule.ID); err != nil {
			return result, err
		}
	}
	if err := settings.expireSeen(ctx, q, stamp); err != nil {
		return result, err
	}
	if len(result.Added) > 0 {
		lines := make([]string, len(result.Added))
		for i, a := range result.Added {
			lines[i] = a.Channel + ": " + a.Title + gb(a.SizeBytes)
		}
		title := "Added " + result.Added[0].Channel
		if len(result.Added) != 1 {
			title = fmt.Sprintf("Added %d releases", len(result.Added))
		}
		on, err := notify.Enabled(ctx, q)
		if err != nil {
			return result, err
		}
		if on {
			message := notify.Message{Title: title, Body: strings.Join(lines, "\n"), Now: func() float64 { return stamp }, Channel: notify.Media}
			if settings.PublicURL != "" {
				message.Click = settings.PublicURL + DownloadsPage
			}
			sent, err := notify.Send(ctx, q, env, message)
			if err != nil {
				return result, err
			}
			if !sent.Delivered {
				result.Errors = append(result.Errors, "ntfy: "+sent.Error)
			}
		}
		if _, err := SelectFiles(ctx, qb, nil); err != nil {
			return result, err
		}
	}
	if opts.Only == nil {
		errs := make([]any, len(result.Errors))
		for i, e := range result.Errors {
			errs[i] = e
		}
		if err := setState(ctx, q, lastCheck, stamp); err != nil {
			return result, err
		}
		if err := setState(ctx, q, lastErrors, errs); err != nil {
			return result, err
		}
	}
	for _, line := range result.Errors {
		log.Warn("channels: " + line)
	}
	return result, nil
}
