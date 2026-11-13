package pages

import (
	"errors"
	"math/big"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/web"
)

type apiListedThread struct {
	ID          int64   `json:"id"`
	Title       *string `json:"title"`
	Pinned      bool    `json:"pinned"`
	Archived    bool    `json:"archived"`
	ProjectID   *int64  `json:"project_id"`
	ProjectName *string `json:"project_name"`
	RunState    *string `json:"run_state"`
	CostUSD     float64 `json:"cost_usd"`
	CreatedAt   float64 `json:"created_at"`
	UpdatedAt   float64 `json:"updated_at"`
}

type apiThread struct {
	ID                   int64    `json:"id"`
	Kind                 string   `json:"kind"`
	Title                *string  `json:"title"`
	Model                string   `json:"model"`
	Effort               string   `json:"effort"`
	Thinking             string   `json:"thinking"`
	ContextLimit         int64    `json:"context_limit"`
	WebSearch            bool     `json:"web_search"`
	CostCapUSD           *float64 `json:"cost_cap_usd"`
	ProjectID            *int64   `json:"project_id"`
	Pinned               bool     `json:"pinned"`
	Archived             bool     `json:"archived"`
	BranchedFromThreadID *int64   `json:"branched_from_thread_id"`
	CreatedAt            float64  `json:"created_at"`
	UpdatedAt            float64  `json:"updated_at"`
}

type apiRun struct {
	ID        int64    `json:"id"`
	State     string   `json:"state"`
	Steps     int64    `json:"steps"`
	Error     *string  `json:"error"`
	StartedAt float64  `json:"started_at"`
	EndedAt   *float64 `json:"ended_at"`
}

type apiQueued struct {
	ID        int64   `json:"id"`
	Text      string  `json:"text"`
	CreatedAt float64 `json:"created_at"`
}

type apiQuestion struct {
	RunID int64  `json:"run_id"`
	Text  string `json:"text"`
}

type apiItem struct {
	Seq        int64                `json:"seq"`
	RunID      *int64               `json:"run_id"`
	CreatedAt  float64              `json:"created_at"`
	Kind       string               `json:"kind"`
	Text       string               `json:"text"`
	Files      []service.Attachment `json:"files"`
	Steps      []*service.Step      `json:"steps"`
	Citations  []service.Citation   `json:"citations"`
	DraftID    *int64               `json:"draft_id,omitempty"`
	ProposalID *int64               `json:"proposal_id,omitempty"`
}

type apiTranscript struct {
	Thread     apiThread     `json:"thread"`
	ActiveRun  *apiRun       `json:"active_run"`
	Queued     []apiQueued   `json:"queued"`
	Question   *apiQuestion  `json:"question"`
	CostUSD    float64       `json:"cost_usd"`
	Meter      service.Meter `json:"meter"`
	Items      []apiItem     `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

type apiNewThread struct {
	Text         string   `json:"text"`
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	ContextLimit *int64   `json:"context_limit"`
	Thinking     string   `json:"thinking"`
	WebSearch    *bool    `json:"web_search"`
	CostCapUSD   *float64 `json:"cost_cap_usd"`
	ProjectID    *int64   `json:"project_id"`
}

type apiMessage struct {
	Text string `json:"text"`
	Edit bool   `json:"edit"`
}

type apiAnswer struct {
	Text string `json:"text"`
}

type seqCursor struct {
	Seq int64 `json:"seq"`
}

func (p *pages) apiRoutes(d *web.Deps) {
	d.API(http.MethodGet, "/threads", p.apiThreads)
	d.API(http.MethodPost, "/threads", bindAssistant(web.Idempotent(p.apiNewThread)).ServeHTTP)
	d.API(http.MethodGet, "/threads/{thread_id}", p.apiTranscript)
	d.API(http.MethodPost, "/threads/{thread_id}/messages", bindAssistant(web.Idempotent(p.apiMessage)).ServeHTTP)
	d.API(http.MethodPost, "/threads/{thread_id}/stop", bindAssistant(http.HandlerFunc(p.apiStop)).ServeHTTP)
	d.API(http.MethodPost, "/threads/{thread_id}/answer", bindAssistant(http.HandlerFunc(p.apiAnswer)).ServeHTTP)
	d.API(http.MethodGet, "/threads/{thread_id}/runs/{run_id}/events", p.apiEvents)
}

func threadOut(t threads.Thread) apiThread {
	return apiThread{
		ID: t.ID, Kind: string(t.Kind), Title: t.Title, Model: t.Model, Effort: t.Effort, Thinking: t.Thinking,
		ContextLimit: threads.ContextLimitOf(t), WebSearch: bool(t.WebSearch), CostCapUSD: t.CostCapUSD, ProjectID: t.ProjectID,
		Pinned: bool(t.Pinned), Archived: bool(t.Archived), BranchedFromThreadID: t.BranchedFromThreadID,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

func runOut(r *journal.Run) *apiRun {
	if r == nil {
		return nil
	}
	return &apiRun{ID: r.ID, State: r.State, Steps: r.Steps, Error: r.Error, StartedAt: r.StartedAt, EndedAt: r.EndedAt}
}

func rowID(v any) *int64 {
	switch n := v.(type) {
	case int64:
		return &n
	case float64:
		id := int64(n)
		return &id
	}
	return nil
}

func itemOut(item *service.Item) apiItem {
	out := apiItem{
		Seq: item.Seq, RunID: item.RunID, CreatedAt: item.CreatedAt, Kind: item.Kind, Text: item.Text,
		Files: item.Files, Steps: item.Steps, Citations: item.Citations,
	}
	if item.Draft != nil {
		out.DraftID = rowID(item.Draft["id"])
	}
	if item.ProposalID != 0 {
		out.ProposalID = &item.ProposalID
	}
	return out
}

func (p *pages) apiRefused(w http.ResponseWriter, err error) bool {
	var exceeded inference.BudgetExceeded
	var active service.RunActive
	var redo service.NothingToRedo
	var unknown service.UnknownThread
	var option threads.OptionError
	var refused references.Refused
	var disabled *module.OwnerDisabled
	var archived asstapp.ArchivedProject
	var readOnly asstapp.ReadOnlyThread
	var app *application.Error
	switch {
	case err == nil:
		return false
	case errors.As(err, &disabled):
		web.APIError(w, http.StatusConflict, "module_disabled", err.Error())
	case errors.As(err, &exceeded):
		web.APIError(w, http.StatusConflict, "budget_exceeded", err.Error())
	case errors.As(err, &refused):
		web.APIError(w, http.StatusUnprocessableEntity, "reference_refused", err.Error())
	case errors.As(err, &active):
		web.APIError(w, http.StatusConflict, "run_active", "Wait for the current reply to finish, then send again.")
	case errors.As(err, &redo):
		web.APIError(w, http.StatusConflict, "nothing_to_edit", err.Error())
	case errors.As(err, &unknown):
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown thread.")
	case errors.As(err, &archived):
		web.APIError(w, http.StatusConflict, "project_archived", err.Error())
	case errors.As(err, &readOnly):
		web.APIError(w, http.StatusConflict, "read_only_thread", "Subagent threads are read-only.")
	case errors.As(err, &app) && app.Code == application.NotConfigured:
		web.APIError(w, http.StatusConflict, "not_configured", err.Error())
	case errors.As(err, &app) && app.Code == application.NotFound:
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown project.")
	case errors.As(err, &option):
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", err.Error())
	default:
		panic(err)
	}
	return true
}

func required(w http.ResponseWriter, name string) {
	web.APIError(w, http.StatusUnprocessableEntity, "invalid", name+" is required.", web.Missing("body", name))
}

func (p *pages) apiThreads(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	view := web.QueryValue(query, "view")
	if view != "" && view != archived {
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", "view must be archived or empty.")
		return
	}
	var projectID int64
	if raw := web.QueryValue(query, "project_id"); raw != "" {
		n, ok := numx.LaxInt64(raw)
		if !ok {
			web.APIError(w, http.StatusUnprocessableEntity, "invalid", "project_id must be an integer.", web.BadInt("query", "project_id", raw))
			return
		}
		projectID = n
	}
	var key threads.ListKey
	limit, after, ok := web.Paging(w, r, &key)
	if !ok {
		return
	}
	in := asstapp.ThreadListInput{Q: strings.TrimSpace(web.QueryValue(query, "q")), Limit: limit, Archived: view == archived, ProjectID: projectID}
	if after {
		in.After = &key
	}
	read, err := p.assistant().ThreadList(r.Context(), in)
	if err != nil {
		web.APIFail(w, err)
		return
	}
	out := web.List[apiListedThread]{Items: []apiListedThread{}}
	if read.Data.Next != nil {
		out.NextCursor = web.EncodeCursor(*read.Data.Next)
	}
	for _, t := range read.Data.Items {
		out.Items = append(out.Items, apiListedThread{
			ID: t.ID, Title: t.Title, Pinned: bool(t.Pinned), Archived: bool(t.Archived), ProjectID: t.ProjectID,
			ProjectName: t.ProjectName, RunState: t.RunState, CostUSD: t.Cost, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		})
	}
	web.APIRead(w, r, read.Revision, out)
}

func (p *pages) apiNewThread(w http.ResponseWriter, r *http.Request) {
	var in apiNewThread
	if !web.DecodeJSON(w, r, &in) {
		return
	}
	ctx := detached(r)
	webSearch := true
	if in.WebSearch != nil {
		webSearch = *in.WebSearch
	}
	start := asstapp.StartInput{
		Kind: threads.Chat, Model: in.Model, Effort: in.Effort, Thinking: in.Thinking,
		WebSearch: &webSearch, CostCapUSD: in.CostCapUSD, Text: in.Text, Operator: true,
	}
	if in.ProjectID != nil {
		start.ProjectID = *in.ProjectID
	}
	if in.ContextLimit != nil {
		start.ContextLimit = *in.ContextLimit
	}
	started, err := p.assistant().ThreadStart(ctx, start)
	if p.apiRefused(w, err) {
		return
	}
	var runID *int64
	if started.Data.RunID != 0 {
		runID = &started.Data.RunID
	}
	thread, err := threads.Get(ctx, p.deps.DB.Read, started.Data.ThreadID)
	check(err)
	web.JSON(w, http.StatusCreated, map[string]any{"thread": threadOut(*thread), "run_id": runID})
}

func (p *pages) apiTranscript(w http.ResponseWriter, r *http.Request) {
	threadID, ok := web.PathID(w, r, "thread_id")
	if !ok {
		return
	}
	var cursor seqCursor
	limit, after, ok := web.Paging(w, r, &cursor)
	if !ok {
		return
	}
	in := asstapp.TranscriptInput{ID: threadID, Limit: limit}
	if after {
		in.Before = cursor.Seq
	}
	read, err := p.assistant().Transcript(r.Context(), in)
	if errorIs(err, application.NotFound) {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown thread.")
		return
	}
	check(err)
	web.APIRead(w, r, read.Revision, transcriptOut(read.Data))
}

func (p *pages) apiMessage(w http.ResponseWriter, r *http.Request) {
	threadID, ok := web.PathID(w, r, "thread_id")
	if !ok {
		return
	}
	var in apiMessage
	if !web.DecodeJSON(w, r, &in) {
		return
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		required(w, "text")
		return
	}
	send := p.assistant().MessageSend
	if in.Edit {
		send = p.assistant().MessageEdit
	}
	change, err := send(detached(r), asstapp.SendInput{ThreadID: threadID, Text: text, Operator: true})
	if p.apiRefused(w, err) {
		return
	}
	if in.Edit {
		web.JSON(w, http.StatusAccepted, map[string]any{"run_id": change.Data.RunID})
		return
	}
	if change.Data.QueuedID != 0 {
		web.JSON(w, http.StatusAccepted, map[string]any{"queued_id": change.Data.QueuedID})
		return
	}
	web.JSON(w, http.StatusAccepted, map[string]any{"run_id": change.Data.RunID})
}

func (p *pages) apiStop(w http.ResponseWriter, r *http.Request) {
	threadID, ok := web.PathID(w, r, "thread_id")
	if !ok {
		return
	}
	ctx := detached(r)
	thread, err := threads.Get(ctx, p.deps.DB.Read, threadID)
	check(err)
	if thread == nil {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown thread.")
		return
	}
	active, err := journal.ActiveRun(ctx, p.deps.DB.Read, threadID)
	check(err)
	stopped := false
	if active != nil {
		stopped, err = journal.RequestStop(ctx, p.deps.DB.Write, active.ID)
		check(err)
	}
	if !stopped {
		web.APIError(w, http.StatusConflict, "no_active_run", "No run is running.")
		return
	}
	web.JSON(w, http.StatusOK, map[string]any{"stopped": true, "run_id": active.ID})
}

func (p *pages) apiAnswer(w http.ResponseWriter, r *http.Request) {
	threadID, ok := web.PathID(w, r, "thread_id")
	if !ok {
		return
	}
	var in apiAnswer
	if !web.DecodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		required(w, "text")
		return
	}
	ctx := detached(r)
	question, err := service.PendingQuestion(ctx, p.deps.DB.Read, threadID)
	check(err)
	if question == nil {
		web.APIError(w, http.StatusConflict, "no_question", "No question is waiting for an answer.")
		return
	}
	if err := p.svc().Answer(ctx, threadID, in.Text); err != nil {
		web.APIError(w, http.StatusConflict, "not_answered", err.Error())
		return
	}
	web.JSON(w, http.StatusAccepted, map[string]any{"run_id": question.RunID})
}

func (p *pages) apiEvents(w http.ResponseWriter, r *http.Request) {
	threadID, ok := web.PathID(w, r, "thread_id")
	if !ok {
		return
	}
	runID, ok := web.PathID(w, r, "run_id")
	if !ok {
		return
	}
	var errs []web.FieldError
	after := queryAfter(r, &errs)
	if len(errs) > 0 {
		web.APIError(w, http.StatusUnprocessableEntity, "invalid", "after must be an integer.", errs...)
		return
	}
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		n, ok := new(big.Int).SetString(header, 10)
		if !ok || !n.IsInt64() {
			web.APIError(w, http.StatusBadRequest, "bad_request", "Last-Event-ID must be an integer.")
			return
		}
		after = n.Int64()
	}
	read, err := p.assistant().RunEvents(r.Context(), asstapp.RunInput{ThreadID: threadID, RunID: runID, After: after})
	if errorIs(err, application.NotFound) {
		web.APIError(w, http.StatusNotFound, "not_found", "Unknown run.")
		return
	}
	check(err)
	p.serveStream(w, r, read.Data)
}

func transcriptOut(v asstapp.Transcript) apiTranscript {
	out := apiTranscript{
		Thread: threadOut(v.Thread), ActiveRun: runOut(v.Active), Queued: []apiQueued{},
		CostUSD: v.Cost, Meter: v.Meter, Items: []apiItem{},
	}
	if v.Earlier != 0 {
		out.NextCursor = web.EncodeCursor(seqCursor{Seq: v.Earlier})
	}
	for _, item := range v.Items {
		out.Items = append(out.Items, itemOut(item))
	}
	for _, q := range v.Queued {
		out.Queued = append(out.Queued, apiQueued{ID: q.ID, Text: q.Text, CreatedAt: q.CreatedAt})
	}
	if v.Question != nil {
		out.Question = &apiQuestion{RunID: v.Question.RunID, Text: v.Question.Text}
	}
	return out
}

func errorIs(err error, code application.Code) bool {
	return err != nil && application.Classify(err).Code == code
}
