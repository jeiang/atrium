package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
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
	if web.FragmentNotModified(w, r, read.Revision) {
		return
	}
	data := knowledgeListOf(read.Data)
	if web.IsPartial(r) {
		p.renderFragment(w, r, http.StatusOK, "_knowledge_rows.gohtml", data, nil)
		return
	}
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge.gohtml", data, nil)
}

func knowledgeListOf(l kbapp.List) knowledgeListData {
	f := l.Filter
	data := knowledgeListData{Q: f.Query, Scope: f.Scope, State: f.State, Rows: l.Rows,
		Bulk: f.State == "unconfirmed" || f.State == "imported"}
	for _, row := range data.Rows {
		data.Unconfirmed = data.Unconfirmed || !row.Confirmed && !row.Retracted
	}
	return data
}

func knowledgeValues(r kbapp.Record) knowledgeFormValues {
	marks := map[string]bool{}
	for _, m := range kbapp.Marks {
		marks[m.Name] = r.Marks&m.Mark != 0
	}
	return knowledgeFormValues{Title: r.Title, Body: r.Body, Aliases: strings.Join(r.Aliases, "\n"),
		Scope: r.Scope, Private: r.Private, Pinned: r.Pinned, Marks: marks}
}

func submittedKnowledge(form url.Values) kbapp.RecordInput {
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
	return kbapp.RecordInput{
		Title:   web.FormString(form, "title", ""),
		Body:    web.FormString(form, "body", ""),
		Aliases: aliases,
		Scope:   web.FormString(form, "scope", kbapp.ScopePersonal),
		Private: web.FormString(form, "private", "") == "1",
		Marks:   marks,
		Pinned:  web.FormString(form, "pinned", "") == "1",
	}
}

func formValuesOf(in kbapp.RecordInput) knowledgeFormValues {
	marks := map[string]bool{}
	for _, m := range kbapp.Marks {
		marks[m.Name] = in.Marks&m.Mark != 0
	}
	return knowledgeFormValues{Title: in.Title, Body: in.Body, Aliases: strings.Join(in.Aliases, "\n"),
		Scope: in.Scope, Private: in.Private, Pinned: in.Pinned, Marks: marks}
}

func (p *pages) knowledgeNew(w http.ResponseWriter, r *http.Request) {
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge_new.gohtml",
		knowledgeFormData{Action: "/library/knowledge/new", Values: knowledgeFormValues{Scope: kbapp.ScopePersonal}}, nil)
}

func (p *pages) knowledgeCreate(w http.ResponseWriter, r *http.Request) {
	in := submittedKnowledge(web.FormValues(r))
	changed, err := p.knowledge().Create(detached(r), in)
	if err != nil {
		web.Fail(w, r, err, func(status int) {
			problem := application.Classify(err).Error()
			p.render(w, r, status, knowledgeFrame, "knowledge_new.gohtml",
				knowledgeFormData{Action: "/library/knowledge/new", Values: formValuesOf(in), Error: &problem}, nil)
		})
		return
	}
	navigate(w, r, knowledgePath(changed.Data.ID))
}

func knowledgePath(id int64) string { return "/library/knowledge/" + strconv.FormatInt(id, 10) }

func (p *pages) knowledgeRecordPage(w http.ResponseWriter, r *http.Request, status int, d kbapp.Detail, form *knowledgeFormData) {
	marks := make([]knowledgeMark, len(kbapp.Marks))
	for i, m := range kbapp.Marks {
		marks[i] = knowledgeMark{Name: m.Name, Label: m.Label, On: d.Record.Marks&m.Mark != 0}
	}
	if form == nil {
		form = &knowledgeFormData{Action: knowledgePath(d.Record.ID), Values: knowledgeValues(d.Record)}
	}
	data := knowledgeRecordData{Record: d.Record, Marks: marks, SameRun: d.SameRun, History: d.History, Source: d.Record.Source, Form: *form}
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
	p.knowledgeRecordPage(w, r, http.StatusOK, read.Data, nil)
}

func (p *pages) knowledgeUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	in := submittedKnowledge(web.FormValues(r))
	changed, err := p.knowledge().Edit(detached(r), kbapp.EditInput{ID: id, RecordInput: in})
	if err != nil {
		current, derr := p.knowledge().Detail(r.Context(), kbapp.DetailInput{ID: id})
		if derr != nil {
			p.knowledgeDetailFail(w, r, derr)
			return
		}
		web.Fail(w, r, err, func(status int) {
			problem := application.Classify(err).Error()
			p.knowledgeRecordPage(w, r, status, current.Data, &knowledgeFormData{Action: knowledgePath(id), Values: formValuesOf(in), Error: &problem})
		})
		return
	}
	navigate(w, r, knowledgePath(changed.Data.ID))
}

func (p *pages) knowledgeConfirm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	changed, err := p.knowledge().Confirm(detached(r), kbapp.IDInput{ID: id})
	if err != nil {
		p.knowledgeRefused(w, r, "Not confirmed", err)
		return
	}
	navigate(w, r, knowledgePath(changed.Data.ID))
}

func (p *pages) knowledgeRefused(w http.ResponseWriter, r *http.Request, title string, err error) {
	web.Fail(w, r, err, func(status int) { p.failureStatus(w, r, status, title, application.Classify(err).Error()) })
}

func (p *pages) knowledgeRetract(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	changed, err := p.knowledge().Retract(detached(r), kbapp.IDInput{ID: id})
	if err != nil {
		p.knowledgeRefused(w, r, "Not retracted", err)
		return
	}
	if web.IsPartial(r) {
		current, err := p.knowledge().Detail(r.Context(), kbapp.DetailInput{ID: id})
		if err != nil {
			p.knowledgeDetailFail(w, r, err)
			return
		}
		p.renderFragment(w, r, http.StatusOK, "_knowledge_retract.gohtml", current.Data, nil)
		return
	}
	navigate(w, r, knowledgePath(changed.Data.ID))
}

func (p *pages) knowledgeRestore(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "record_id")
	if !ok {
		return
	}
	changed, err := p.knowledge().Restore(detached(r), kbapp.IDInput{ID: id})
	if err != nil {
		p.knowledgeRefused(w, r, "Not restored", err)
		return
	}
	navigate(w, r, knowledgePath(changed.Data.ID))
}

func (p *pages) knowledgeRunRetract(w http.ResponseWriter, r *http.Request) {
	runID, errs := pathInt(r, "run_id")
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	_, err := p.knowledge().RetractRun(detached(r), kbapp.RunInput{RunID: runID})
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
	result, err := p.knowledge().ConfirmSelected(detached(r), kbapp.SelectedInput{IDs: ids})
	if err != nil {
		p.knowledgeBatchRefused(w, r, filter, result.Data, err)
		return
	}
	if refused := result.Data.Refused; len(refused) > 0 {
		p.knowledgeBatchRefused(w, r, filter, result.Data, &application.Error{Code: refused[0].Code, Message: refused[0].Message})
		return
	}
	navigate(w, r, "/library/knowledge?"+filterQuery(filter))
}

func (p *pages) knowledgeConfirmAll(w http.ResponseWriter, r *http.Request) {
	filter := formFilter(web.FormValues(r))
	result, err := p.knowledge().ConfirmMatching(detached(r), filter)
	if err != nil {
		p.knowledgeBatchRefused(w, r, filter, result.Data, err)
		return
	}
	read, err := p.knowledge().List(r.Context(), filter)
	check(err)
	committed, refused := len(result.Data.Committed), len(result.Data.Refused)
	toast := notice("success", "Confirmed", fmt.Sprintf("%d confirmed, %d refused", committed, refused))
	if refused > 0 {
		toast.Category = "warning"
	}
	w.Header().Set("HX-Push-Url", "/library/knowledge?"+filterQuery(read.Data.Filter))
	p.render(w, r, http.StatusOK, knowledgeFrame, "knowledge.gohtml", knowledgeListOf(read.Data), toast)
}

func (p *pages) knowledgeBatchRefused(w http.ResponseWriter, r *http.Request, filter kbapp.ListInput, result kbapp.BatchResult, err error) {
	read, readErr := p.knowledge().List(r.Context(), filter)
	if readErr != nil {
		p.knowledgeRefused(w, r, "Nothing confirmed", err)
		return
	}
	title := "Nothing confirmed"
	if committed := len(result.Committed); committed > 0 {
		title = fmt.Sprintf("%d confirmed", committed)
	}
	toast := notice("warning", title, fmt.Sprintf("%d confirmed, %d refused. %s", len(result.Committed), len(result.Refused), application.Classify(err).Error()))
	web.Fail(w, r, err, func(status int) {
		p.render(w, r, status, knowledgeFrame, "knowledge.gohtml", knowledgeListOf(read.Data), toast)
	})
}

func (p *pages) knowledgeProfilePage(w http.ResponseWriter, r *http.Request, status int, problem *string) {
	read, err := p.knowledge().Profile(r.Context())
	check(err)
	p.render(w, r, status, knowledgeFrame, "knowledge_profile.gohtml", knowledgeProfileData{Profile: read.Data, Error: problem}, nil)
}

func (p *pages) knowledgeProfile(w http.ResponseWriter, r *http.Request) {
	p.knowledgeProfilePage(w, r, http.StatusOK, nil)
}

func (p *pages) knowledgeProfileAdd(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	in := kbapp.RecordInput{
		Title:  web.FormString(form, "title", ""),
		Body:   web.FormString(form, "body", ""),
		Scope:  kbapp.ScopePersonal,
		Pinned: true,
	}
	if _, err := p.knowledge().Create(detached(r), in); err != nil {
		web.Fail(w, r, err, func(status int) {
			problem := application.Classify(err).Error()
			p.knowledgeProfilePage(w, r, status, &problem)
		})
		return
	}
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
	if _, err := p.knowledge().Purge(detached(r), kbapp.IDInput{ID: id}); err != nil {
		p.knowledgeRefused(w, r, "Not purged", err)
		return
	}
	navigate(w, r, "/library/knowledge")
}
