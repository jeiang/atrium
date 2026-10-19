// Package report is report.py: the shadow-mode report of what the router proposed against where each message
// ended up, and the counts the triage view shares.
package report

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	maxDisagreementLines = 40
	TopDests             = 15
	Gone                 = "(gone)"
)

var sinceRe = rx.MustCompile(`^(\d+)([hd])$`, 0)

const liveCopy = "(SELECT l.%s FROM messages l WHERE l.account = m.account AND l.digest = m.digest " +
	"AND l.gone_at IS NULL ORDER BY l.id DESC LIMIT 1)"

var ActualSQL = fmt.Sprintf(liveCopy, "location") + " AS actual, " + fmt.Sprintf(liveCopy, "in_inbox") + " AS actual_inbox"

var OutcomeSQL = "SELECT d.id AS decision_id, d.stage, d.action, d.dest, m.id AS message_id, m.msgid, m.from_domain, " +
	ActualSQL + " FROM decisions d JOIN messages m ON m.id = d.message_id WHERE d.account = ? AND d.ts >= ? ORDER BY d.id"

type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SinceError is the ValueError parse_since raises on a malformed value.
type SinceError string

func (e SinceError) Error() string { return string(e) }

func (SinceError) PyClass() string { return "ValueError" }

func sinceError(value string) error {
	return SinceError(fmt.Sprintf("time data %s does not match format '%%Y-%%m-%%d'", py.Repr(value)))
}

// ParseSince is parse_since: nil is the epoch, "<n>h" and "<n>d" count back from at, and a date is midnight in
// the zone ATRIUM_TZ names.
func ParseSince(env config.Env, value *string, at time.Time) (float64, error) {
	now := float64(at.UnixNano()) / 1e9
	if value == nil {
		return 0, nil
	}
	m, err := sinceRe.Match(*value)
	if err != nil {
		return 0, err
	}
	if m != nil {
		n := new(big.Int)
		for _, r := range m.Group(1) {
			n.Mul(n, big.NewInt(10)).Add(n, big.NewInt(int64(py.DigitValue(r))))
		}
		if m.Group(2) == "h" {
			n.Mul(n, big.NewInt(3600))
		} else {
			n.Mul(n, big.NewInt(86400))
		}
		span, _ := new(big.Float).SetInt(n).Float64()
		if math.IsInf(span, 0) {
			return 0, errors.New("int too large to convert to float")
		}
		return now - span, nil
	}
	day, ok := py.StrptimeDate(*value)
	if !ok {
		return 0, sinceError(*value)
	}
	loc, err := config.Location(env)
	if err != nil {
		return 0, err
	}
	return day.Timestamp(loc), nil
}

// Row is one OutcomeSQL row.
type Row struct {
	DecisionID  int64
	Stage       string
	Action      string
	Dest        sql.NullString
	MessageID   int64
	MsgID       string
	FromDomain  string
	Actual      sql.NullString
	ActualInbox sql.NullInt64
}

func Outcomes(ctx context.Context, q Querier, account string, since float64) ([]Row, error) {
	rows, err := q.QueryContext(ctx, OutcomeSQL, account, since)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Row, error) {
		var row Row
		err := r.Scan(&row.DecisionID, &row.Stage, &row.Action, &row.Dest, &row.MessageID, &row.MsgID, &row.FromDomain,
			&row.Actual, &row.ActualInbox)
		return row, err
	})
}

// Outcome is outcome: where the message's live copy is now.
func (r Row) Outcome() string {
	switch {
	case !r.Actual.Valid:
		return Gone
	case r.ActualInbox.Int64 != 0:
		return config.Inbox
	}
	return r.Actual.String
}

// Matches is matches: the proposed destination is one of the live copy's locations.
func (r Row) Matches() bool {
	return r.Dest.Valid && slices.Contains(strings.Split(r.Actual.String, "|"), r.Dest.String)
}

// Counter is collections.Counter's insertion order, which most_common keeps for ties.
type Counter[K comparable] struct {
	keys   []K
	counts map[K]int
}

func (c *Counter[K]) Add(k K) {
	if c.counts == nil {
		c.counts = map[K]int{}
	}
	if _, ok := c.counts[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.counts[k]++
}

func (c *Counter[K]) Get(k K) int { return c.counts[k] }

func (c *Counter[K]) Total() int {
	total := 0
	for _, n := range c.counts {
		total += n
	}
	return total
}

// Entry is one most_common pair.
type Entry[K comparable] struct {
	Key   K
	Count int
}

// MostCommon is Counter.most_common(n): highest count first, ties in insertion order.
func (c *Counter[K]) MostCommon(n int) []Entry[K] {
	out := make([]Entry[K], len(c.keys))
	for i, k := range c.keys {
		out[i] = Entry[K]{k, c.counts[k]}
	}
	slices.SortStableFunc(out, func(a, b Entry[K]) int { return b.Count - a.Count })
	return out[:min(n, len(out))]
}

// Tally is what the report and the triage summary count from the outcome rows.
type Tally struct {
	Stages        Counter[string]
	Dests         Counter[string]
	Deletes       int
	NoLabel       int
	Inbox, Gone   int
	Moved, Agreed []Row
}

func Count(rows []Row) Tally {
	var t Tally
	for _, r := range rows {
		t.Stages.Add(r.Stage)
		if r.Action == "file" && r.Dest.String != "" {
			t.Dests.Add(r.Dest.String)
		}
		switch r.Action {
		case "delete":
			t.Deletes++
		case "none":
			t.NoLabel++
		}
		switch r.Outcome() {
		case config.Inbox:
			t.Inbox++
		case Gone:
			t.Gone++
		default:
			t.Moved = append(t.Moved, r)
			if r.Matches() {
				t.Agreed = append(t.Agreed, r)
			}
		}
	}
	return t
}

func accountSection(ctx context.Context, q Querier, account string, since float64) ([]string, error) {
	rows, err := Outcomes(ctx, q, account, since)
	if err != nil {
		return nil, err
	}
	t := Count(rows)
	out := []string{fmt.Sprintf("== %s ==", account), fmt.Sprintf("decisions: %d", len(rows))}
	stages := make([]string, len(router.Stages))
	for i, s := range router.Stages {
		stages[i] = fmt.Sprintf("%s=%d", s, t.Stages.Get(s))
	}
	out = append(out, "by stage: "+strings.Join(stages, ", "))
	out = append(out, fmt.Sprintf("proposed moves: %d, proposed deletes: %d, no label: %d", t.Dests.Total(), t.Deletes, t.NoLabel))
	for _, e := range t.Dests.MostCommon(TopDests) {
		out = append(out, fmt.Sprintf("  %5d  %s", e.Count, e.Key))
	}
	line := fmt.Sprintf("outcome: still in inbox=%d, gone=%d, moved=%d, proposal equals actual=%d",
		t.Inbox, t.Gone, len(t.Moved), len(t.Agreed))
	if len(t.Moved) > 0 {
		line += fmt.Sprintf(" (%.1f%%)", float64(100*len(t.Agreed))/float64(len(t.Moved)))
	}
	out = append(out, line)
	var pairs []Row
	for _, r := range t.Moved {
		if !r.Matches() {
			pairs = append(pairs, r)
		}
	}
	out = append(out, disagreementLines("disagreements (proposed -> actual)", pairs)...)
	section, err := flagSection(ctx, q, account, since)
	return append(out, section...), err
}

type disagreement struct{ domain, proposed, actual string }

// DisagreementLines is disagreement_lines over moved rows whose proposal differs from where they are.
func disagreementLines(title string, pairs []Row) []string {
	out := []string{fmt.Sprintf("%s: %d", title, len(pairs))}
	var grouped Counter[disagreement]
	for _, r := range pairs {
		proposed := r.Dest.String
		if proposed == "" {
			proposed = "-"
		}
		grouped.Add(disagreement{r.FromDomain, proposed, r.Actual.String})
	}
	for _, e := range grouped.MostCommon(maxDisagreementLines) {
		out = append(out, fmt.Sprintf("  %4d  %s: %s -> %s", e.Count, e.Key.domain, e.Key.proposed, e.Key.actual))
	}
	return out
}

// FlagCounts is flag_counts.
type FlagCounts struct {
	Considered   int `json:"considered"`
	Contacts     int `json:"contacts"`
	LLM          int `json:"llm"`
	Judged       int `json:"judged"`
	Both         int `json:"both"`
	ContactsOnly int `json:"contacts_only"`
	LLMOnly      int `json:"llm_only"`
}

func CountFlags(ctx context.Context, q Querier, account string, since float64) (FlagCounts, error) {
	rows, err := q.QueryContext(ctx, "SELECT contacts_match, llm_flag FROM flag_decisions WHERE account = ? AND ts >= ?", account, since)
	if err != nil {
		return FlagCounts{}, err
	}
	type decision struct {
		contacts int
		llm      sql.NullInt64
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (decision, error) {
		var d decision
		err := r.Scan(&d.contacts, &d.llm)
		return d, err
	})
	if err != nil {
		return FlagCounts{}, err
	}
	n := FlagCounts{Considered: len(all)}
	for _, d := range all {
		n.Contacts += d.contacts
		if !d.llm.Valid {
			continue
		}
		n.Judged++
		n.LLM += int(d.llm.Int64)
		switch {
		case d.contacts != 0 && d.llm.Int64 != 0:
			n.Both++
		case d.contacts != 0:
			n.ContactsOnly++
		case d.llm.Int64 != 0:
			n.LLMOnly++
		}
	}
	return n, nil
}

func flagSection(ctx context.Context, q Querier, account string, since float64) ([]string, error) {
	n, err := CountFlags(ctx, q, account, since)
	return []string{fmt.Sprintf(
		"flags (unread inbox): considered=%d, contacts would-flag=%d, 9B flag=%d of %d judged, both=%d, contacts-only=%d, 9B-only=%d",
		n.Considered, n.Contacts, n.LLM, n.Judged, n.Both, n.ContactsOnly, n.LLMOnly)}, err
}

// AccountExists is the "SELECT 1 FROM accounts" probe the report and the triage summaries make per account.
func AccountExists(ctx context.Context, q Querier, name string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM accounts WHERE name = ?", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Build is build: the report for every configured account that has a row in accounts.
func Build(ctx context.Context, q Querier, env config.Env, since *string, at time.Time) (string, error) {
	start, err := ParseSince(env, since, at)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, spec := range config.Accounts {
		ok, err := AccountExists(ctx, q, spec.Name)
		if err != nil {
			return "", err
		}
		if !ok {
			continue
		}
		section, err := accountSection(ctx, q, spec.Name, start)
		if err != nil {
			return "", err
		}
		lines = append(append(lines, section...), "")
	}
	return strings.TrimRightFunc(strings.Join(lines, "\n"), py.IsSpace) + "\n", nil
}
