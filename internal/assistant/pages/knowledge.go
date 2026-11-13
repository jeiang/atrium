package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/kb"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const knowledgeFrame = "library_knowledge"

type knowledgeListData struct {
	Q     string
	Scope string
	State string
	Rows  []kbapp.Row
	Bulk  bool

	Unconfirmed bool
}

type knowledgeRecordData struct {
	Record  kbapp.Record
	Marks   []knowledgeMark
	SameRun []kbapp.Row
	History []kbapp.HistoryRow
	Source  *kbapp.Source
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

type knowledgeProfileData struct {
	kbapp.Profile
	Error *string
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

func knowledgeFilter(q url.Values) kbapp.ListInput {
	return kbapp.ListInput{Query: web.QueryValue(q, "q"), Scope: web.QueryValue(q, "scope"), State: web.QueryValue(q, "state")}
}

func formFilter(form url.Values) kbapp.ListInput {
	return kbapp.ListInput{Query: web.FormString(form, "q", ""), Scope: web.FormString(form, "scope", ""), State: web.FormString(form, "state", "")}
}

func filterQuery(f kbapp.ListInput) string {
	return url.Values{"q": {f.Query}, "scope": {f.Scope}, "state": {f.State}}.Encode()
}

func (p *pages) knowledgeList(w http.ResponseWriter, r *http.Request) {
	filter := knowledgeFilter(r.URL.Query())
	read, err := p.knowledge().List(r.Context(), filter)
	if err != nil {
		web.Fail(w, r, err, func(status int) {
			toast := &tpl.Toast{Category: "error", Title: "Invalid filter", Description: application.Classify(err).Error()}
			p.render(w, r, status, knowledgeFrame, "knowledge.gohtml", knowledgeListData{Q: filter.Query, Scope: filter.Scope, State: filter.State}, toast)
		})
		return
	}
	if web.NotModified(w, r, read.Revision) {
		return
	}
	f := read.Data.Filter
	data := knowledgeListData{Q: f.Query, Scope: f.Scope, State: f.State, Rows: read.Data.Rows,
		Bulk: f.State == "unconfirmed" || f.State == "imported"}
	for _, row := range data.Rows {
		data.Unconfirmed = data.Unconfirmed || !row.Confirmed && !row.Retracted
	}
	if web.IsPartial(r) {
		p.renderFragment(w, r, http.StatusOK, "_knowledge_rows.gohtml", data, nil)
		return
	}
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge.gohtml", data, nil)
}

func knowledgeValues(r kbapp.Record) knowledgeFormValues {
	marks := map[string]bool{}
	for _, m := range kbapp.Marks {
		marks[m.Name] = r.Marks&m.Mark != 0
	}
	return knowledgeFormValues{Title: r.Title, Body: r.Body, Aliases: strings.Join(r.Aliases, "\n"),
		Scope: r.Scope, Private: r.Private, Pinned: r.Pinned, Marks: marks}
}

func submittedKnowledge(form url.Values) kb.EditInput {
	marks := tool.Marks(0)
	for _, m := range kbapp.Marks {
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
	for _, m := range kbapp.Marks {
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

func (p *pages) knowledgeRecordPage(w http.ResponseWriter, r *http.Request, status int, d kbapp.Detail, problem *string) {
	marks := make([]knowledgeMark, len(kbapp.Marks))
	for i, m := range kbapp.Marks {
		marks[i] = knowledgeMark{Name: m.Name, Label: m.Label, On: d.Record.Marks&m.Mark != 0}
	}
	data := knowledgeRecordData{Record: d.Record, Marks: marks, SameRun: d.SameRun, History: d.History, Source: d.Record.Source,
		Form: knowledgeFormData{Action: knowledgePath(d.Record.ID), Values: knowledgeValues(d.Record), Error: problem}}
	p.render(w, r, status, knowledgeFrame, "knowledge_record.gohtml", data, nil)
}

func (p *pages) knowledgeDetailFail(w http.ResponseWriter, r *http.Request, err error) {
	web.Fail(w, r, err, func(status int) { p.fail(w, r, status, application.Classify(err).Error()) })
}

func (p *pages) knowledgeRecord(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	read, err := p.knowledge().Detail(r.Context(), kbapp.DetailInput{ID: id})
	if err != nil {
		p.knowledgeDetailFail(w, r, err)
		return
	}
	if web.NotModified(w, r, read.Revision) {
		return
	}
	p.knowledgeRecordPage(w, r, http.StatusOK, read.Data, nil)
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
		current, err := p.knowledge().Detail(r.Context(), kbapp.DetailInput{ID: id})
		if err != nil {
			p.knowledgeDetailFail(w, r, err)
			return
		}
		p.knowledgeRecordPage(w, r, http.StatusUnprocessableEntity, current.Data, &problem)
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
	if web.IsPartial(r) {
		current, err := p.knowledge().Detail(r.Context(), kbapp.DetailInput{ID: rec.ID})
		if err != nil {
			p.knowledgeDetailFail(w, r, err)
			return
		}
		p.renderFragment(w, r, http.StatusOK, "_knowledge_retract.gohtml", current.Data, nil)
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
	filter, err := formFilter(form).Normalize()
	if err != nil {
		p.failure(w, r, "Nothing confirmed", application.Classify(err).Error())
		return
	}
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
	navigate(w, r, "/library/knowledge?"+filterQuery(filter))
}

func (p *pages) knowledgeConfirmAll(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	filter, err := formFilter(form).Normalize()
	if err != nil {
		p.failure(w, r, "Nothing confirmed", application.Classify(err).Error())
		return
	}
	if filter.State != "unconfirmed" && filter.State != "imported" {
		p.failure(w, r, "Nothing confirmed", "Choose the unconfirmed or imported filter first.")
		return
	}
	found, err := p.knowledge().List(r.Context(), filter)
	check(err)
	for _, rec := range found.Data.Rows {
		if rec.Confirmed || rec.Retracted {
			continue
		}
		if _, _, err := kb.Confirm(detached(r), p.store(), rec.ID, p.now()); err != nil {
			check(err)
		}
	}
	navigate(w, r, "/library/knowledge?"+filterQuery(filter))
}

func (p *pages) knowledgeProfilePage(w http.ResponseWriter, r *http.Request, status int, problem *string) {
	read, err := p.knowledge().Profile(r.Context())
	check(err)
	p.render(w, r, status, knowledgeFrame, "knowledge_profile.gohtml", knowledgeProfileData{Profile: read.Data, Error: problem}, nil)
}

func (p *pages) knowledgeProfile(w http.ResponseWriter, r *http.Request) {
	read, err := p.knowledge().Profile(r.Context())
	check(err)
	if web.NotModified(w, r, read.Revision) {
		return
	}
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge_profile.gohtml", knowledgeProfileData{Profile: read.Data}, nil)
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
	read, err := p.knowledge().Detail(r.Context(), kbapp.DetailInput{ID: id})
	if err != nil {
		p.knowledgeDetailFail(w, r, err)
		return
	}
	p.renderFragment(w, r, http.StatusOK, "_knowledge_purge.gohtml", read.Data.Record, nil)
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
