package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tokens"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

const knowledgeFrame = "library_knowledge"

var knowledgeStates = []string{"all", "unconfirmed", "pinned", "imported", "retracted"}
var knowledgeScopes = []string{"", kb.ScopeWork, kb.ScopePersonal}

var knowledgeMarks = []struct {
	Name  string
	Label string
	Mark  tool.Marks
}{
	{"untrusted", "Untrusted", tool.Untrusted},
	{"pii", "PII", tool.PII},
	{"messages", "Messages", tool.Messages},
	{"personal", "Personal", tool.Personal},
}

type knowledgeRow struct {
	ID        int64
	Title     string
	Scope     string
	Writer    string
	Marks     []string
	Confirmed bool
	Pinned    bool
	Retracted bool
	Updated   float64
}

type knowledgeListData struct {
	Q     string
	Scope string
	State string
	Rows  []knowledgeRow
	Bulk  bool

	Unconfirmed bool
}

type knowledgeSource struct {
	ThreadID int64
	Seq      *int64
	Origin   string
	NotionID string
}

type knowledgeRecordData struct {
	Record  kb.Record
	Marks   []knowledgeMark
	SameRun []knowledgeRow
	History []kb.WriteRow
	Source  *knowledgeSource
	Form    knowledgeFormData
}

type knowledgeMark struct {
	Name  string
	Label string
	On    bool
}

type knowledgeFormValues struct {
	Title   string
	Body    string
	Aliases string
	Scope   string
	Private bool
	Pinned  bool
	Marks   map[string]bool
}

type knowledgeFormData struct {
	Action string
	Title  string
	Values knowledgeFormValues
	Error  *string
}

type knowledgeProfileEntry struct {
	ID       int64
	Title    string
	Body     string
	Reason   string
	InPrompt bool
	Tokens   int64
}

type knowledgeProfileData struct {
	Block   string
	Tokens  int64
	Budget  int64
	Percent int64
	Entries []knowledgeProfileEntry
	Error   *string
}

func markNames(marks tool.Marks) []string {
	out := []string{}
	for _, m := range knowledgeMarks {
		if marks&m.Mark != 0 {
			out = append(out, m.Name)
		}
	}
	return out
}

func knowledgeRowOf(r kb.Record) knowledgeRow {
	return knowledgeRow{ID: r.ID, Title: r.Title, Scope: r.Scope, Writer: r.Writer, Marks: markNames(r.Marks),
		Confirmed: r.Confirmed(), Pinned: r.Pinned, Retracted: r.Retracted(), Updated: r.UpdatedAt}
}

func rowsOf(records []kb.Record) []knowledgeRow {
	out := make([]knowledgeRow, len(records))
	for i, r := range records {
		out[i] = knowledgeRowOf(r)
	}
	return out
}

func (p *pages) store() *ledger.Store { return p.svc().Runner.Ledger }

func (p *pages) knowledgeRoutes(r chi.Router) {
	r.Get("/library/knowledge", p.knowledgeList)
	r.Get("/library/knowledge/new", p.knowledgeNew)
	r.Post("/library/knowledge/new", p.knowledgeCreate)
	r.Post("/library/knowledge/confirm-all", p.knowledgeConfirmAll)
	r.Post("/library/knowledge/confirm", p.knowledgeConfirmSelected)
	r.Get("/library/knowledge/profile", p.knowledgeProfile)
	r.Post("/library/knowledge/profile", p.knowledgeProfileAdd)
	r.Post("/library/knowledge/runs/{run_id}/retract", p.knowledgeRunRetract)
	r.Get("/library/knowledge/{record_id}", p.knowledgeRecord)
	r.Post("/library/knowledge/{record_id}", p.knowledgeUpdate)
	r.Post("/library/knowledge/{record_id}/confirm", p.knowledgeConfirm)
	r.Post("/library/knowledge/{record_id}/retract", p.knowledgeRetract)
	r.Post("/library/knowledge/{record_id}/restore", p.knowledgeRestore)
	p.deps.Authenticated(http.MethodGet, "/library/knowledge/{record_id}/purge", p.knowledgePurgeDialog)
	p.deps.Authenticated(http.MethodPost, "/library/knowledge/{record_id}/purge", p.knowledgePurge)
}

func knowledgeFilter(q url.Values) (query, scope, state string) {
	query = web.QueryValue(q, "q")
	scope = web.QueryValue(q, "scope")
	if scope != kb.ScopeWork && scope != kb.ScopePersonal {
		scope = ""
	}
	state = web.QueryValue(q, "state")
	valid := false
	for _, s := range knowledgeStates {
		if state == s {
			valid = true
		}
	}
	if !valid {
		state = "all"
	}
	return query, scope, state
}

func (p *pages) knowledgeList(w http.ResponseWriter, r *http.Request) {
	query, scope, state := knowledgeFilter(r.URL.Query())
	found, err := kb.List(r.Context(), p.deps.DB.Read, kb.ListFilter{Query: query, Scope: scope, State: state})
	check(err)
	data := knowledgeListData{Q: query, Scope: scope, State: state, Rows: rowsOf(found),
		Bulk: state == "unconfirmed" || state == "imported"}
	for _, row := range data.Rows {
		data.Unconfirmed = data.Unconfirmed || !row.Confirmed && !row.Retracted
	}
	if web.IsPartial(r) {
		p.renderFragment(w, r, http.StatusOK, "_knowledge_rows.gohtml", data, nil)
		return
	}
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge.gohtml", data, nil)
}

func knowledgeValues(r kb.Record) knowledgeFormValues {
	marks := map[string]bool{}
	for _, m := range knowledgeMarks {
		marks[m.Name] = r.Marks&m.Mark != 0
	}
	return knowledgeFormValues{Title: r.Title, Body: r.Body, Aliases: strings.Join(r.Aliases, "\n"),
		Scope: r.Scope, Private: r.Private, Pinned: r.Pinned, Marks: marks}
}

func submittedKnowledge(form url.Values) kb.EditInput {
	marks := tool.Marks(0)
	for _, m := range knowledgeMarks {
		if web.FormString(form, m.Name, "") == "1" {
			marks |= m.Mark
		}
	}
	aliases := []string{}
	for _, line := range strings.Split(web.FormString(form, "aliases", ""), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			aliases = append(aliases, line)
		}
	}
	return kb.EditInput{
		Title:   strings.TrimSpace(web.FormString(form, "title", "")),
		Body:    strings.TrimSpace(web.FormString(form, "body", "")),
		Aliases: aliases,
		Scope:   web.FormString(form, "scope", kb.ScopePersonal),
		Private: web.FormString(form, "private", "") == "1",
		Marks:   marks,
		Pinned:  web.FormString(form, "pinned", "") == "1",
	}
}

func formValuesOf(in kb.EditInput) knowledgeFormValues {
	marks := map[string]bool{}
	for _, m := range knowledgeMarks {
		marks[m.Name] = in.Marks&m.Mark != 0
	}
	return knowledgeFormValues{Title: in.Title, Body: in.Body, Aliases: strings.Join(in.Aliases, "\n"),
		Scope: in.Scope, Private: in.Private, Pinned: in.Pinned, Marks: marks}
}

func (p *pages) knowledgeNew(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge_new.gohtml",
		knowledgeFormData{Action: "/library/knowledge/new", Values: knowledgeFormValues{Scope: kb.ScopePersonal}}, nil)
}

func (p *pages) knowledgeCreate(w http.ResponseWriter, r *http.Request) {
	in := submittedKnowledge(web.FormValues(r))
	rec, _, err := kb.OperatorCreate(detached(r), p.store(), in, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		problem := err.Error()
		p.render(w, r, http.StatusUnprocessableEntity, knowledgeFrame, "knowledge_new.gohtml",
			knowledgeFormData{Action: "/library/knowledge/new", Values: formValuesOf(in), Error: &problem}, nil)
		return
	}
	check(err)
	navigate(w, r, knowledgePath(rec.ID))
}

func knowledgePath(id int64) string { return "/library/knowledge/" + strconv.FormatInt(id, 10) }

func (p *pages) knowledgeRecordPage(w http.ResponseWriter, r *http.Request, status int, rec kb.Record, problem *string) {
	same, err := kb.SameRun(r.Context(), p.deps.DB.Read, valueOf(rec.SourceRunID), rec.ID)
	check(err)
	history, err := kb.History(r.Context(), p.deps.DB.Read, rec.ID)
	check(err)
	marks := make([]knowledgeMark, len(knowledgeMarks))
	for i, m := range knowledgeMarks {
		marks[i] = knowledgeMark{Name: m.Name, Label: m.Label, On: rec.Marks&m.Mark != 0}
	}
	var source *knowledgeSource
	if rec.Origin != nil {
		source = &knowledgeSource{Origin: *rec.Origin}
		if strings.HasPrefix(*rec.Origin, "notion:") {
			source.NotionID = strings.TrimPrefix(*rec.Origin, "notion:")
		}
	} else if rec.SourceThreadID != nil {
		source = &knowledgeSource{ThreadID: *rec.SourceThreadID, Seq: rec.SourceSeq}
	}
	data := knowledgeRecordData{Record: rec, Marks: marks, SameRun: rowsOf(same), History: history, Source: source,
		Form: knowledgeFormData{Action: knowledgePath(rec.ID), Values: knowledgeValues(rec), Error: problem}}
	p.render(w, r, status, knowledgeFrame, "knowledge_record.gohtml", data, nil)
}

func (p *pages) knowledgeRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	rec, err := kb.Get(r.Context(), p.deps.DB.Read, id)
	check(err)
	if rec == nil {
		p.fail(w, r, http.StatusNotFound, "unknown knowledge record")
		return
	}
	p.knowledgeRecordPage(w, r, http.StatusOK, *rec, nil)
}

func (p *pages) knowledgeUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	in := submittedKnowledge(web.FormValues(r))
	rec, _, err := kb.OperatorEdit(detached(r), p.store(), id, in, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		problem := err.Error()
		current, gerr := kb.Get(r.Context(), p.deps.DB.Read, id)
		check(gerr)
		if current == nil {
			p.fail(w, r, http.StatusNotFound, "unknown knowledge record")
			return
		}
		p.knowledgeRecordPage(w, r, http.StatusUnprocessableEntity, *current, &problem)
		return
	}
	check(err)
	navigate(w, r, knowledgePath(rec.ID))
}

func (p *pages) knowledgeConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	rec, _, err := kb.Confirm(detached(r), p.store(), id, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		p.failure(w, r, "Not confirmed", err.Error())
		return
	}
	check(err)
	navigate(w, r, knowledgePath(rec.ID))
}

func (p *pages) knowledgeRetract(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	rec, _, err := kb.OperatorRetract(detached(r), p.store(), id, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		p.failure(w, r, "Not retracted", err.Error())
		return
	}
	check(err)
	same, err := kb.SameRun(r.Context(), p.deps.DB.Read, valueOf(rec.SourceRunID), rec.ID)
	check(err)
	data := struct {
		Record  kb.Record
		SameRun []knowledgeRow
	}{Record: rec, SameRun: rowsOf(same)}
	if web.IsPartial(r) {
		p.renderFragment(w, r, http.StatusOK, "_knowledge_retract.gohtml", data, nil)
		return
	}
	navigate(w, r, knowledgePath(rec.ID))
}

func (p *pages) knowledgeRestore(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	rec, _, err := kb.Restore(detached(r), p.store(), id, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		p.failure(w, r, "Not restored", err.Error())
		return
	}
	check(err)
	navigate(w, r, knowledgePath(rec.ID))
}

func (p *pages) knowledgeRunRetract(w http.ResponseWriter, r *http.Request) {
	runID, errs := pathInt(r, "run_id")
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	_, err := kb.RunRetract(detached(r), p.store(), runID, p.now())
	check(err)
	navigate(w, r, "/library/knowledge")
}

func (p *pages) knowledgeConfirmSelected(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	query, scope, state := knowledgeFilter(url.Values{"q": {web.FormString(form, "q", "")}, "scope": {web.FormString(form, "scope", "")}, "state": {web.FormString(form, "state", "")}})
	ids := make([]int64, 0, len(form["id"]))
	for _, raw := range form["id"] {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			web.Unprocessable(w, web.BadInt("body", "id", raw))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		p.failure(w, r, "Nothing confirmed", "Select at least one record.")
		return
	}
	for _, id := range ids {
		_, _, err := kb.Confirm(detached(r), p.store(), id, p.now())
		var refused kb.Refused
		if errors.As(err, &refused) {
			p.failure(w, r, "Not confirmed", err.Error())
			return
		}
		check(err)
	}
	navigate(w, r, "/library/knowledge?"+url.Values{"q": {query}, "scope": {scope}, "state": {state}}.Encode())
}

func (p *pages) knowledgeConfirmAll(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	query, scope, state := knowledgeFilter(url.Values{"q": {web.FormString(form, "q", "")}, "scope": {web.FormString(form, "scope", "")}, "state": {web.FormString(form, "state", "")}})
	if state != "unconfirmed" && state != "imported" {
		p.failure(w, r, "Nothing confirmed", "Choose the unconfirmed or imported filter first.")
		return
	}
	found, err := kb.List(r.Context(), p.deps.DB.Read, kb.ListFilter{Query: query, Scope: scope, State: state})
	check(err)
	for _, rec := range found {
		if rec.Confirmed() || rec.Retracted() {
			continue
		}
		if _, _, err := kb.Confirm(detached(r), p.store(), rec.ID, p.now()); err != nil {
			check(err)
		}
	}
	navigate(w, r, "/library/knowledge?"+url.Values{"q": {query}, "scope": {scope}, "state": {state}}.Encode())
}

func (p *pages) knowledgeProfilePage(w http.ResponseWriter, r *http.Request, status int, problem *string) {
	block, err := kb.ProfileBlock(r.Context(), p.deps.DB.Read, 0)
	check(err)
	budget := int64(assistant.TuningAgentKbProfileTokens)
	entries, err := kb.ProfileReview(r.Context(), p.deps.DB.Read, budget)
	check(err)
	rows := make([]knowledgeProfileEntry, len(entries))
	for i, e := range entries {
		rows[i] = knowledgeProfileEntry{ID: e.ID, Title: e.Title, Body: e.Body, Reason: e.Reason, InPrompt: e.InPrompt, Tokens: e.Tokens}
	}
	n := tokens.Estimate(block)
	percent := int64(0)
	if budget > 0 {
		percent = min(n*100/budget, 100)
	}
	data := knowledgeProfileData{Block: block, Tokens: n, Budget: budget, Percent: percent, Entries: rows, Error: problem}
	p.render(w, r, status, knowledgeFrame, "knowledge_profile.gohtml", data, nil)
}

func (p *pages) knowledgeProfile(w http.ResponseWriter, r *http.Request) {
	p.knowledgeProfilePage(w, r, http.StatusOK, nil)
}

func (p *pages) knowledgeProfileAdd(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	in := kb.EditInput{
		Title:  strings.TrimSpace(web.FormString(form, "title", "")),
		Body:   strings.TrimSpace(web.FormString(form, "body", "")),
		Scope:  kb.ScopePersonal,
		Pinned: true,
	}
	_, _, err := kb.OperatorCreate(detached(r), p.store(), in, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		problem := err.Error()
		p.knowledgeProfilePage(w, r, http.StatusUnprocessableEntity, &problem)
		return
	}
	check(err)
	navigate(w, r, "/library/knowledge/profile")
}

func (p *pages) knowledgePurgeDialog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	rec, err := kb.Get(r.Context(), p.deps.DB.Read, id)
	check(err)
	if rec == nil {
		p.fail(w, r, http.StatusNotFound, "unknown knowledge record")
		return
	}
	p.renderFragment(w, r, http.StatusOK, "_knowledge_purge.gohtml", rec, nil)
}

func (p *pages) knowledgePurge(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	err := kb.Purge(detached(r), p.store(), id, p.now())
	var refused kb.Refused
	if errors.As(err, &refused) {
		p.failure(w, r, "Not purged", err.Error())
		return
	}
	check(err)
	navigate(w, r, "/library/knowledge")
}

func valueOf(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
