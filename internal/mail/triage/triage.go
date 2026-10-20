package triage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/report"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/py"
)

var Agreements = [...]string{"agree", "disagree", "pending", "gone"}

const TopDests = 15

const agreementSQL = "CASE WHEN actual IS NULL THEN 'gone' WHEN actual_inbox = 1 THEN 'pending' " +
	"WHEN dest IS NOT NULL AND instr('|' || actual || '|', '|' || dest || '|') > 0 THEN 'agree' " +
	"ELSE 'disagree' END"

var latestSQL = "SELECT d.id AS decision_id, d.ts, d.stage, d.key, d.action, d.dest, d.confidence, d.detail, " +
	"m.id AS message_id, m.account, m.subject, m.from_addr, m.from_name, m.date_ts, m.seen, m.flagged, " +
	report.ActualSQL + ", c.folder AS corrected_to, " +
	"aa.id AS action_id, aa.kind AS action_kind, aa.ok AS action_ok, aa.error AS action_error, " +
	"aa.undone_ts AS action_undone_ts, aa.undo IS NOT NULL AS action_has_undo " +
	"FROM decisions d JOIN messages m ON m.id = d.message_id " +
	"LEFT JOIN corrections c ON c.message_id = m.id " +
	"LEFT JOIN auto_actions aa ON aa.id = (SELECT MAX(y.id) FROM auto_actions y " +
	"WHERE y.message_id = m.id AND y.kind IN ('file', 'trash', 'correct')) " +
	"WHERE d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = d.message_id)"

var base = "SELECT *, " + agreementSQL + " AS agreement FROM (" + latestSQL + ")"

type Applied struct {
	ID       int64   `json:"id"`
	Kind     string  `json:"kind"`
	Status   string  `json:"status"`
	Error    *string `json:"error"`
	Undoable bool    `json:"undoable"`
}

type Row struct {
	DecisionID     int64    `json:"decision_id"`
	TS             float64  `json:"ts"`
	Stage          string   `json:"stage"`
	Key            *string  `json:"key"`
	Action         string   `json:"action"`
	Dest           *string  `json:"dest"`
	Confidence     *float64 `json:"confidence"`
	Detail         *string  `json:"detail"`
	MessageID      int64    `json:"message_id"`
	Account        string   `json:"account"`
	Subject        string   `json:"subject"`
	FromAddr       string   `json:"from_addr"`
	FromName       string   `json:"from_name"`
	DateTS         float64  `json:"date_ts"`
	Seen           bool     `json:"seen"`
	Flagged        bool     `json:"flagged"`
	Actual         *string  `json:"actual"`
	ActualInbox    *int64   `json:"actual_inbox"`
	CorrectedTo    *string  `json:"corrected_to"`
	ActionID       *int64   `json:"action_id"`
	ActionKind     *string  `json:"action_kind"`
	ActionOK       *int64   `json:"action_ok"`
	ActionError    *string  `json:"action_error"`
	ActionUndoneTS *float64 `json:"action_undone_ts"`
	ActionHasUndo  int64    `json:"action_has_undo"`
	Agreement      string   `json:"agreement"`
	Current        *string  `json:"current"`
	Applied        *Applied `json:"applied"`
}

func scanRow(r *sql.Rows) (Row, error) {
	var row Row
	var seen, flagged int64
	if err := r.Scan(&row.DecisionID, &row.TS, &row.Stage, &row.Key, &row.Action, &row.Dest, &row.Confidence, &row.Detail,
		&row.MessageID, &row.Account, &row.Subject, &row.FromAddr, &row.FromName, &row.DateTS, &seen, &flagged,
		&row.Actual, &row.ActualInbox, &row.CorrectedTo, &row.ActionID, &row.ActionKind, &row.ActionOK, &row.ActionError,
		&row.ActionUndoneTS, &row.ActionHasUndo, &row.Agreement); err != nil {
		return row, err
	}
	row.Seen, row.Flagged = seen != 0, flagged != 0
	row.Current = current(row)
	row.Applied = applied(row)
	return row, nil
}

func current(r Row) *string {
	if r.Actual == nil {
		return nil
	}
	if r.ActualInbox != nil && *r.ActualInbox != 0 {
		inbox := "INBOX"
		return &inbox
	}
	return r.Actual
}

func applied(r Row) *Applied {
	if r.ActionID == nil {
		return nil
	}
	a := &Applied{ID: *r.ActionID, Status: live.StateOf(r.ActionOK, r.ActionUndoneTS), Error: r.ActionError,
		Undoable: r.ActionOK != nil && (*r.ActionOK == 0 || *r.ActionOK == 1) && r.ActionUndoneTS == nil && r.ActionHasUndo != 0}
	if r.ActionKind != nil {
		a.Kind = *r.ActionKind
	}
	return a
}

type Filter struct {
	Account   string
	Stage     string
	Agreement string
	Limit     int
	Offset    int
	Q         string
}

type FilterError string

func (e FilterError) Error() string { return string(e) }

func (FilterError) PyClass() string { return "ValueError" }

type Page struct {
	Total int   `json:"total"`
	Rows  []Row `json:"rows"`
}

func Triage(ctx context.Context, q report.Querier, f Filter) (Page, error) {
	if f.Stage != "" && !slices.Contains(router.Stages[:], f.Stage) {
		return Page{}, FilterError("unknown stage " + py.Repr(f.Stage))
	}
	if f.Agreement != "" && !slices.Contains(Agreements[:], f.Agreement) {
		return Page{}, FilterError("unknown agreement " + py.Repr(f.Agreement))
	}
	if f.Limit == 0 {
		f.Limit = 50
	}
	var clauses []string
	var params []any
	if f.Account != "" {
		clauses = append(clauses, "account = ?")
		params = append(params, f.Account)
	}
	if f.Stage != "" {
		clauses = append(clauses, "stage = ?")
		params = append(params, f.Stage)
	}
	if f.Agreement != "" {
		clauses = append(clauses, agreementSQL+" = ?")
		params = append(params, f.Agreement)
	}
	if f.Q != "" {
		clauses = append(clauses,
			"instr(lower(coalesce(subject, '') || char(10) || coalesce(from_addr, '') || char(10) || coalesce(from_name, '') || char(10) || "+
				"coalesce(actual, '') || char(10) || coalesce(dest, '') || char(10) || coalesce(corrected_to, '') || char(10) || "+
				"coalesce(key, '') || char(10) || coalesce(stage, '') || char(10) || coalesce(account, '')), lower(?)) > 0")
		params = append(params, f.Q)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var page Page
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+base+")"+where, params...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := q.QueryContext(ctx, "SELECT * FROM ("+base+")"+where+" ORDER BY decision_id DESC LIMIT ? OFFSET ?",
		append(slices.Clone(params), f.Limit, f.Offset)...)
	if err != nil {
		return page, err
	}
	page.Rows, err = db.Collect(rows, scanRow)
	if page.Rows == nil {
		page.Rows = []Row{}
	}
	return page, err
}

func Get(ctx context.Context, q report.Querier, messageID int64) (*Row, error) {
	rows, err := q.QueryContext(ctx, "SELECT * FROM ("+base+") WHERE message_id = ?", messageID)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, scanRow)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return &found[0], nil
}

type Destination struct {
	Dest  string
	Count int
}

func (d Destination) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "[%s,%d]", jsonString(d.Dest), d.Count), nil
}

type Summary struct {
	Account         string            `json:"account"`
	Decisions       int               `json:"decisions"`
	ByStage         map[string]int    `json:"by_stage"`
	ProposedMoves   int               `json:"proposed_moves"`
	ProposedDeletes int               `json:"proposed_deletes"`
	NoLabel         int               `json:"no_label"`
	TopDestinations []Destination     `json:"top_destinations"`
	StillInInbox    int               `json:"still_in_inbox"`
	Gone            int               `json:"gone"`
	Moved           int               `json:"moved"`
	Agreed          int               `json:"agreed"`
	AgreementPct    *float64          `json:"agreement_pct"`
	Flags           report.FlagCounts `json:"flags"`
}

func Summarize(ctx context.Context, q report.Querier, env config.Env, account string, since *string, at time.Time) (Summary, error) {
	start, err := report.ParseSince(env, since, at)
	if err != nil {
		return Summary{}, err
	}
	rows, err := report.Outcomes(ctx, q, account, start)
	if err != nil {
		return Summary{}, err
	}
	t := report.Count(rows)
	s := Summary{
		Account: account, Decisions: len(rows), ByStage: map[string]int{}, ProposedMoves: t.Dests.Total(),
		ProposedDeletes: t.Deletes, NoLabel: t.NoLabel, TopDestinations: []Destination{}, StillInInbox: t.Inbox,
		Gone: t.Gone, Moved: len(t.Moved), Agreed: len(t.Agreed),
	}
	for _, stage := range router.Stages {
		s.ByStage[stage] = t.Stages.Get(stage)
	}
	for _, e := range t.Dests.MostCommon(TopDests) {
		s.TopDestinations = append(s.TopDestinations, Destination{e.Key, e.Count})
	}
	if len(t.Moved) > 0 {
		pct := float64(100*len(t.Agreed)) / float64(len(t.Moved))
		s.AgreementPct = &pct
	}
	s.Flags, err = report.CountFlags(ctx, q, account, start)
	return s, err
}

func Summaries(ctx context.Context, q report.Querier, env config.Env, since *string, at time.Time) ([]Summary, error) {
	out := []Summary{}
	for _, spec := range config.Accounts {
		ok, err := report.AccountExists(ctx, q, spec.Name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		s, err := Summarize(ctx, q, env, spec.Name, since, at)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

type Judgment struct {
	MessageID            int64   `json:"message_id"`
	Account              string  `json:"account"`
	TS                   float64 `json:"ts"`
	ContactsMatch        int64   `json:"contacts_match"`
	PersonAsking         *int64  `json:"person_asking"`
	DeadlineOrPaymentDue *int64  `json:"deadline_or_payment_due"`
	SecurityEvent        *int64  `json:"security_event"`
	LLMFlag              *int64  `json:"llm_flag"`
	Subject              string  `json:"subject"`
	FromAddr             string  `json:"from_addr"`
	FromName             string  `json:"from_name"`
	DateTS               float64 `json:"date_ts"`
}

func FlagJudgments(ctx context.Context, q report.Querier, account string, limit, offset int) ([]Judgment, error) {
	if limit == 0 {
		limit = 50
	}
	query := "SELECT f.message_id, f.account, f.ts, f.contacts_match, f.person_asking, f.deadline_or_payment_due, " +
		"f.security_event, f.llm_flag, m.subject, m.from_addr, m.from_name, m.date_ts " +
		"FROM flag_decisions f JOIN messages m ON m.id = f.message_id " +
		"WHERE m.gone_at IS NULL AND m.in_inbox = 1 AND m.seen = 0"
	var params []any
	if account != "" {
		query += " AND f.account = ?"
		params = append(params, account)
	}
	query += " ORDER BY f.message_id DESC LIMIT ? OFFSET ?"
	rows, err := q.QueryContext(ctx, query, append(params, limit, offset)...)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (Judgment, error) {
		var j Judgment
		err := r.Scan(&j.MessageID, &j.Account, &j.TS, &j.ContactsMatch, &j.PersonAsking, &j.DeadlineOrPaymentDue,
			&j.SecurityEvent, &j.LLMFlag, &j.Subject, &j.FromAddr, &j.FromName, &j.DateTS)
		return j, err
	})
	if out == nil {
		out = []Judgment{}
	}
	return out, err
}

type Folder struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

func Folders(ctx context.Context, q report.Querier, account string) ([]Folder, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT folder, COUNT(*) AS n FROM filed_messages WHERE account = ? GROUP BY folder ORDER BY folder", account)
	if err != nil {
		return nil, err
	}
	out, err := db.Collect(rows, func(r *sql.Rows) (Folder, error) {
		var f Folder
		err := r.Scan(&f.Folder, &f.Count)
		return f, err
	})
	if out == nil {
		out = []Folder{}
	}
	return out, err
}

func jsonString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}
