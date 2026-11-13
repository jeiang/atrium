package assistantmodule

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/references"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/workspace"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type apiPages struct {
	deps      *web.Deps
	assistant func() *asstapp.Service
	tools     registry.Source
}

func threadAPI(tools registry.Source) func(*web.Deps, func() *asstapp.Service) {
	return func(d *web.Deps, assistant func() *asstapp.Service) {
		p := &apiPages{deps: d, assistant: assistant, tools: tools}
		apiBindings(p)
		fileAPIBindings(p)
		projectAPIBindings(p)
		scheduleAPIBindings(p)
		memoryAPIBindings(p)
		subagentAPIBindings(p)
		searchAPIBindings(p)
	}
}

func errorIs(err error, code application.Code) bool {
	return err != nil && application.Classify(err).Code == code
}

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
	Seq        int64              `json:"seq"`
	RunID      *int64             `json:"run_id"`
	CreatedAt  float64            `json:"created_at"`
	Kind       string             `json:"kind"`
	Text       string             `json:"text"`
	Files      []apiAttachment    `json:"files"`
	Steps      []*apiStep         `json:"steps"`
	Citations  []service.Citation `json:"citations"`
	DraftID    *int64             `json:"draft_id,omitempty"`
	ProposalID *int64             `json:"proposal_id,omitempty"`
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
	Uploads      []files.UploadedFile `json:"-"`
	Text         string               `json:"text,omitempty"`
	Model        string               `json:"model"`
	Effort       string               `json:"effort"`
	ContextLimit *int64               `json:"context_limit"`
	Thinking     string               `json:"thinking,omitempty"`
	WebSearch    *bool                `json:"web_search"`
	CostCapUSD   *float64             `json:"cost_cap_usd"`
	ProjectID    *int64               `json:"project_id"`
}

type apiMessage struct {
	Uploads []files.UploadedFile `json:"-"`
	Text    string               `json:"text"`
	Edit    bool                 `json:"edit,omitempty"`
}

type apiAnswer struct {
	Text string `json:"text"`
}

type seqCursor struct {
	Seq int64 `json:"seq"`
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
		Files: attachmentsOut(item.Files), Steps: stepsOut(item.Steps), Citations: item.Citations,
	}
	if item.Draft != nil {
		out.DraftID = rowID(item.Draft["id"])
	}
	if item.ProposalID != 0 {
		out.ProposalID = &item.ProposalID
	}
	return out
}

type apiAttachment struct {
	ID        json.RawMessage `json:"id"`
	Name      json.RawMessage `json:"name"`
	MediaType json.RawMessage `json:"media_type"`
	Kind      json.RawMessage `json:"kind"`
}
type apiSearchResult struct {
	URL   json.RawMessage `json:"url"`
	Title json.RawMessage `json:"title"`
}
type apiStep struct {
	Kind          string            `json:"kind"`
	Text          string            `json:"text"`
	ID            json.RawMessage   `json:"id"`
	Name          string            `json:"name"`
	Args          string            `json:"args"`
	State         *string           `json:"state"`
	Result        *string           `json:"result"`
	IsError       bool              `json:"is_error"`
	ChildThreadID json.RawMessage   `json:"child_thread_id"`
	Results       []apiSearchResult `json:"results"`
	Error         json.RawMessage   `json:"error"`
}

func apiRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
func attachmentsOut(in []service.Attachment) []apiAttachment {
	if in == nil {
		return nil
	}
	out := make([]apiAttachment, len(in))
	for i, a := range in {
		out[i] = apiAttachment{apiRaw(a.ID), apiRaw(a.Name), apiRaw(a.MediaType), apiRaw(a.Kind)}
	}
	return out
}
func stepsOut(in []*service.Step) []*apiStep {
	if in == nil {
		return nil
	}
	out := make([]*apiStep, len(in))
	for i, s := range in {
		if s == nil {
			continue
		}
		var results []apiSearchResult
		if s.Results != nil {
			results = make([]apiSearchResult, len(s.Results))
			for j, v := range s.Results {
				results[j] = apiSearchResult{apiRaw(v.URL), apiRaw(v.Title)}
			}
		}
		out[i] = &apiStep{Kind: s.Kind, Text: s.Text, ID: apiRaw(s.ID), Name: s.Name, Args: s.Args, State: s.State, Result: s.Result, IsError: s.IsError, ChildThreadID: apiRaw(s.ChildThreadID), Results: results, Error: apiRaw(s.Error)}
	}
	return out
}

type apiThreadListRequest struct {
	Q         string     `query:"q" json:"-"`
	View      string     `query:"view" json:"-"`
	ProjectID int64      `query:"project_id" api:"legacy-int" json:"-"`
	Limit     api.Limit  `query:"limit" json:"-"`
	Cursor    api.Cursor `query:"cursor" json:"-"`
}

type apiTranscriptRequest struct {
	ThreadID int64      `path:"thread_id" json:"-"`
	Limit    api.Limit  `query:"limit" json:"-"`
	Cursor   api.Cursor `query:"cursor" json:"-"`
}

type apiEventsRequest struct {
	ThreadID    int64  `path:"thread_id" json:"-"`
	RunID       int64  `path:"run_id" json:"-"`
	After       string `query:"after" json:"-"`
	LastEventID string `header:"Last-Event-ID" json:"-"`
}

type apiStreamEvent struct {
	RunID int64  `json:"run_id"`
	State string `json:"state,omitempty"`
}

type apiThreadRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
}

type apiRunRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
	RunID    int64 `path:"run_id" json:"-"`
}

type apiHistoryRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
	Start    int64 `query:"start" api:"min=0" json:"-"`
	N        int64 `query:"n" api:"min=0" json:"-"`
}

type apiMessageRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
	apiMessage
}

type apiEditRequest struct {
	ThreadID int64                `path:"thread_id" json:"-"`
	Uploads  []files.UploadedFile `json:"-"`
	Text     string               `json:"text"`
}

type apiAnswerRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
	apiAnswer
}

type apiBranchRequest struct {
	ThreadID int64  `path:"thread_id" json:"-"`
	Seq      *int64 `json:"seq"`
}

type apiPinRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
	Pinned   *bool `json:"pinned"`
}

type apiArchiveRequest struct {
	ThreadID int64 `path:"thread_id" json:"-"`
	Archived *bool `json:"archived"`
}

type apiMoveRequest struct {
	ThreadID  int64  `path:"thread_id" json:"-"`
	ProjectID *int64 `json:"project_id"`
}

type apiOptionsRequest struct {
	ThreadID     int64  `path:"thread_id" json:"-"`
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	ContextLimit *int64 `json:"context_limit"`
}

type apiComposerRequest struct {
	Model        string `query:"model" json:"-"`
	Effort       string `query:"effort" json:"-"`
	Thinking     string `query:"thinking" json:"-"`
	ContextLimit string `query:"context_limit" json:"-"`
	WebSearch    *bool  `query:"web_search" json:"-"`
	CostCap      string `query:"cost_cap" json:"-"`
	Reset        bool   `query:"reset" json:"-"`
}

type apiHomeRequest struct {
	Q     string `query:"q" json:"-"`
	View  string `query:"view" api:"enum=archived|all" json:"-"`
	About string `query:"about" json:"-"`
}

type apiAboutRequest struct {
	Ref string `query:"ref" json:"-"`
}

type apiStarted struct {
	Thread apiThread `json:"thread"`
	RunID  *int64    `json:"run_id"`
}

type apiSent struct {
	RunID    *int64 `json:"run_id,omitempty"`
	QueuedID *int64 `json:"queued_id,omitempty"`
}

type apiStopped struct {
	Stopped bool  `json:"stopped"`
	RunID   int64 `json:"run_id"`
}

type apiRunStarted struct {
	RunID int64 `json:"run_id"`
}

type apiWork struct {
	Needs     []asstapp.Need     `json:"needs"`
	Plan      *workspace.Plan    `json:"plan"`
	Sources   []workspace.Source `json:"sources"`
	Subagents []subagents.Card   `json:"subagents"`
	Files     []files.Summary    `json:"files"`
	Drafts    []json.RawMessage  `json:"drafts"`
	Code      []json.RawMessage  `json:"code"`
}

type apiHistoryLine struct {
	ThreadID int64  `json:"thread_id"`
	Text     string `json:"text"`
	From     int64  `json:"from"`
	To       int64  `json:"to"`
	N        int64  `json:"n"`
	State    string `json:"state"`
	Leaf     bool   `json:"leaf"`
}

type apiHistory struct {
	Lines []apiHistoryLine `json:"lines"`
	Leaf  *string          `json:"leaf"`
}

type apiHome struct {
	Threads  []threads.Listed     `json:"threads"`
	Q        string               `json:"q"`
	View     string               `json:"view"`
	Welcome  bool                 `json:"welcome"`
	Date     string               `json:"date"`
	Digest   []block.DigestLine   `json:"digest"`
	Agenda   bool                 `json:"agenda"`
	About    *asstapp.About       `json:"about"`
	Defaults threads.Options      `json:"defaults"`
	Composer asstapp.ComposerView `json:"composer"`
}

type apiAgenda struct {
	Agenda *asstapp.Agenda `json:"agenda"`
}

type apiAbout struct {
	About *asstapp.About `json:"about"`
}

type apiToday struct {
	ThreadID int64 `json:"thread_id"`
}

func invalidField(name, message string) error {
	return &application.Error{Code: application.Invalid, Message: message, Fields: []application.Field{{Name: name, Code: "missing", Message: "Field required"}}}
}

func reason(code application.Code, wire, message string, err error) error {
	return &application.Error{Code: code, Reason: wire, Message: message, Cause: err}
}

func apiErr(err error) error {
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
		return nil
	case errors.As(err, &disabled):
		return reason(application.Conflict, "module_disabled", "The assistant module is disabled.", err)
	case errors.As(err, &exceeded):
		return reason(application.Budget, "", "The spending limit would be exceeded.", err)
	case errors.As(err, &refused):
		return reason(application.Invalid, "reference_refused", "A reference could not be used.", err)
	case errors.As(err, &active):
		return reason(application.Conflict, "run_active", "Wait for the current reply to finish, then send again.", err)
	case errors.As(err, &redo):
		return reason(application.Conflict, "nothing_to_edit", "There is no message to edit.", err)
	case errors.As(err, &unknown):
		return reason(application.NotFound, "", "Unknown thread.", err)
	case errors.As(err, &archived):
		return reason(application.Conflict, "project_archived", "This project is archived.", err)
	case errors.As(err, &readOnly):
		return reason(application.Conflict, "read_only_thread", "Subagent threads are read-only.", err)
	case errors.As(err, new(asstapp.NoActiveRun)):
		return reason(application.Conflict, "no_active_run", "No run is running.", err)
	case errors.As(err, new(asstapp.NoQuestion)):
		return reason(application.Conflict, "no_question", "No question is waiting for an answer.", err)
	case errors.As(err, new(asstapp.NotAnswered)):
		return reason(application.Conflict, "not_answered", "The answer could not be accepted.", err)
	case errors.As(err, new(service.NotInterrupted)):
		return reason(application.Conflict, "", "The run cannot be resumed.", err)
	case errors.As(err, &option):
		return reason(application.Invalid, "", "The thread options are not valid.", err)
	case errors.As(err, &app) && app.Code == application.NotConfigured:
		return reason(application.Conflict, "not_configured", "Configure the assistant connection before sending.", err)
	}
	return err
}

func apiQuery[I, O any](p *apiPages, def application.Definition, shape application.APIShape, target string, run func(context.Context, *asstapp.Service, I) (application.Read[O], error)) application.Binding {
	var d *web.Deps
	if p != nil {
		d = p.deps
	}
	shape.Response = reflect.TypeFor[O]()
	return api.Query(d, def, shape, target, func(ctx context.Context, in I) (application.Read[O], error) {
		out, err := run(ctx, p.assistant(), in)
		return out, apiErr(err)
	})
}

func apiCommand[I, O any](p *apiPages, def application.Definition, shape application.APIShape, target string, run func(context.Context, *asstapp.Service, I) (application.Change[O], error)) application.Binding {
	var d *web.Deps
	if p != nil {
		d = p.deps
	}
	shape.Response = reflect.TypeFor[O]()
	shape.BeforeBody = func(ctx context.Context) error {
		_, err := module.BindOwner(ctx, module.Assistant)
		return apiErr(err)
	}
	return api.Command(d, def, shape, target, func(ctx context.Context, in I) (application.Change[O], error) {
		ctx, err := module.BindOwner(ctx, module.Assistant)
		if err != nil {
			return application.Change[O]{}, apiErr(err)
		}
		out, err := run(ctx, p.assistant(), in)
		return out, apiErr(err)
	})
}

func readAs[A, B any](read application.Read[A], err error, conv func(A) B) (application.Read[B], error) {
	if err != nil {
		return application.Read[B]{}, err
	}
	return application.Read[B]{Revision: read.Revision, Data: conv(read.Data)}, nil
}

func changeAs[A, B any](change application.Change[A], err error, conv func(A) B) (application.Change[B], error) {
	if err != nil {
		return application.Change[B]{}, err
	}
	return application.Change[B]{Data: conv(change.Data), AuditID: change.AuditID}, nil
}

func errs(codes ...application.Code) []application.Code { return codes }

func apiBindings(p *apiPages) []application.Binding {
	threadsArea := func(codes ...application.Code) application.APIShape {
		return application.APIShape{Area: "threads", Errors: codes}
	}
	accepted := func(codes ...application.Code) application.APIShape {
		return application.APIShape{Area: "threads", Success: 202, Errors: codes}
	}
	bodiless := func(shape application.APIShape) application.APIShape {
		shape.Body = application.BodyNone
		return shape
	}
	sends := errs(application.NotFound, application.Invalid, application.Conflict, application.Budget)
	stopShape := bodiless(threadsArea(application.NotFound, application.Conflict))
	stopShape.IgnoreBody = true
	return []application.Binding{
		apiQuery(p, asstapp.DescribeThreadList(), threadsArea(application.Invalid), "GET /threads",
			func(ctx context.Context, svc *asstapp.Service, in apiThreadListRequest) (application.Read[web.List[apiListedThread]], error) {
				if in.View != "" && in.View != "archived" {
					return application.Read[web.List[apiListedThread]]{}, &application.Error{Code: application.Invalid, Message: "view must be archived or empty."}
				}
				list := asstapp.ThreadListInput{Q: strings.TrimSpace(in.Q), Limit: int(in.Limit), Archived: in.View == "archived", ProjectID: in.ProjectID}
				if in.Cursor.Set() {
					var key threads.ListKey
					if err := in.Cursor.Last(&key); err != nil {
						return application.Read[web.List[apiListedThread]]{}, err
					}
					list.After = &key
				}
				read, err := svc.ThreadList(ctx, list)
				return readAs(read, err, func(v asstapp.ThreadList) web.List[apiListedThread] {
					out := web.List[apiListedThread]{Items: []apiListedThread{}}
					if v.Next != nil {
						out.NextCursor = in.Cursor.Next(*v.Next)
					}
					for _, t := range v.Items {
						out.Items = append(out.Items, listedOut(t))
					}
					return out
				})
			}),
		{Kind: application.API, Target: "GET /threads/{thread_id}/runs/{run_id}/events", Uses: []application.Name{asstapp.RunEventsName}, API: &application.APIShape{Area: "threads", Request: reflect.TypeFor[apiEventsRequest](), Response: reflect.TypeFor[apiStreamEvent](), Body: application.BodyEventStream, Success: 200, Errors: []application.Code{application.NotFound, application.RateLimited}}},
		apiQuery(p, asstapp.DescribeThreadTranscript(), threadsArea(application.NotFound, application.Invalid), "GET /threads/{thread_id}",
			func(ctx context.Context, svc *asstapp.Service, in apiTranscriptRequest) (application.Read[apiTranscript], error) {
				req := asstapp.TranscriptInput{ID: in.ThreadID, Limit: int(in.Limit)}
				if in.Cursor.Set() {
					var c seqCursor
					if err := in.Cursor.Last(&c); err != nil {
						return application.Read[apiTranscript]{}, err
					}
					req.Before = c.Seq
				}
				read, err := svc.Transcript(ctx, req)
				return readAs(read, err, func(v asstapp.Transcript) apiTranscript { return transcriptOut(v, in.Cursor) })
			}),
		apiQuery(p, asstapp.DescribeThreadWork(), threadsArea(application.NotFound), "GET /threads/{thread_id}/work",
			func(ctx context.Context, svc *asstapp.Service, in apiThreadRequest) (application.Read[apiWork], error) {
				read, err := svc.Work(ctx, asstapp.ThreadInput{ID: in.ThreadID})
				return readAs(read, err, workOut)
			}),
		apiQuery(p, asstapp.DescribeThreadHistory(), threadsArea(application.NotFound, application.Invalid), "GET /threads/{thread_id}/history",
			func(ctx context.Context, svc *asstapp.Service, in apiHistoryRequest) (application.Read[apiHistory], error) {
				read, err := svc.History(ctx, asstapp.HistoryInput{ThreadID: in.ThreadID, Start: in.Start, N: in.N})
				return readAs(read, err, historyOut)
			}),
		apiQuery(p, asstapp.DescribeRunDetail(), threadsArea(application.NotFound), "GET /threads/{thread_id}/runs/{run_id}",
			func(ctx context.Context, svc *asstapp.Service, in apiRunRequest) (application.Read[apiRun], error) {
				read, err := svc.RunDetail(ctx, asstapp.RunInput{ThreadID: in.ThreadID, RunID: in.RunID})
				return readAs(read, err, func(r journal.Run) apiRun { return *runOut(&r) })
			}),
		apiCommand(p, asstapp.DescribeThreadStart(), multipartJSONShape("threads", 201, sends...), "POST /threads",
			func(ctx context.Context, svc *asstapp.Service, in apiNewThread) (application.Change[apiStarted], error) {
				if len(in.Uploads) > 0 && strings.TrimSpace(in.Text) == "" {
					return application.Change[apiStarted]{}, invalidField("text", "text is required.")
				}
				webSearch := true
				if in.WebSearch != nil {
					webSearch = *in.WebSearch
				}
				start := asstapp.StartInput{
					Kind: threads.Chat, Model: in.Model, Effort: in.Effort, Thinking: in.Thinking,
					WebSearch: &webSearch, CostCapUSD: in.CostCapUSD, Text: in.Text, Files: in.Uploads, Operator: true,
				}
				if in.ProjectID != nil {
					start.ProjectID = *in.ProjectID
				}
				if in.ContextLimit != nil {
					start.ContextLimit = *in.ContextLimit
				}
				started, err := svc.ThreadStart(ctx, start)
				if err != nil {
					return application.Change[apiStarted]{}, err
				}
				thread, err := threads.Get(ctx, p.deps.DB.Read, started.Data.ThreadID)
				if err != nil {
					return application.Change[apiStarted]{}, err
				}
				out := apiStarted{Thread: threadOut(*thread)}
				if started.Data.RunID != 0 {
					out.RunID = &started.Data.RunID
				}
				return application.Change[apiStarted]{Data: out, AuditID: started.AuditID}, nil
			}),
		apiCommand(p, asstapp.DescribeMessageSend(), multipartJSONShape("threads", 202, sends...), "POST /threads/{thread_id}/messages",
			func(ctx context.Context, svc *asstapp.Service, in apiMessageRequest) (application.Change[apiSent], error) {
				text := strings.TrimSpace(in.Text)
				if text == "" {
					return application.Change[apiSent]{}, invalidField("text", "text is required.")
				}
				send := svc.MessageSend
				if in.Edit {
					send = svc.MessageEdit
				}
				change, err := send(ctx, asstapp.SendInput{ThreadID: in.ThreadID, Text: text, Files: in.Uploads, Operator: true})
				return changeAs(change, err, func(s asstapp.Sent) apiSent {
					if !in.Edit && s.QueuedID != 0 {
						return apiSent{QueuedID: &s.QueuedID}
					}
					return apiSent{RunID: &s.RunID}
				})
			}),
		apiCommand(p, asstapp.DescribeMessageEdit(), multipartJSONShape("threads", 202, sends...), "PATCH /threads/{thread_id}/messages/latest",
			func(ctx context.Context, svc *asstapp.Service, in apiEditRequest) (application.Change[apiRunStarted], error) {
				text := strings.TrimSpace(in.Text)
				if text == "" {
					return application.Change[apiRunStarted]{}, invalidField("text", "text is required.")
				}
				change, err := svc.MessageEdit(ctx, asstapp.SendInput{ThreadID: in.ThreadID, Text: text, Files: in.Uploads, Operator: true})
				return changeAs(change, err, func(s asstapp.Sent) apiRunStarted { return apiRunStarted{RunID: s.RunID} })
			}),
		apiCommand(p, asstapp.DescribeRunStop(), stopShape, "POST /threads/{thread_id}/stop",
			func(ctx context.Context, svc *asstapp.Service, in apiThreadRequest) (application.Change[apiStopped], error) {
				change, err := svc.RunStop(ctx, asstapp.ThreadInput{ID: in.ThreadID})
				return changeAs(change, err, func(c asstapp.ThreadChanged) apiStopped { return apiStopped{Stopped: true, RunID: c.RunID} })
			}),
		apiCommand(p, asstapp.DescribeRunAnswer(), accepted(application.NotFound, application.Invalid, application.Conflict), "POST /threads/{thread_id}/answer",
			func(ctx context.Context, svc *asstapp.Service, in apiAnswerRequest) (application.Change[apiRunStarted], error) {
				if strings.TrimSpace(in.Text) == "" {
					return application.Change[apiRunStarted]{}, invalidField("text", "text is required.")
				}
				change, err := svc.RunAnswer(ctx, asstapp.AnswerInput{ThreadID: in.ThreadID, Text: in.Text})
				return changeAs(change, err, func(c asstapp.ThreadChanged) apiRunStarted { return apiRunStarted{RunID: c.RunID} })
			}),
		apiCommand(p, asstapp.DescribeReplyRegenerate(), bodiless(accepted(sends...)), "POST /threads/{thread_id}/regenerate",
			func(ctx context.Context, svc *asstapp.Service, in apiThreadRequest) (application.Change[asstapp.ThreadChanged], error) {
				return svc.ReplyRegenerate(ctx, asstapp.ThreadInput{ID: in.ThreadID})
			}),
		apiCommand(p, asstapp.DescribeThreadBranch(), application.APIShape{Area: "threads", Success: 201, Errors: errs(application.NotFound, application.Invalid, application.Conflict)}, "POST /threads/{thread_id}/branch",
			func(ctx context.Context, svc *asstapp.Service, in apiBranchRequest) (application.Change[asstapp.ThreadChanged], error) {
				if in.Seq == nil {
					return application.Change[asstapp.ThreadChanged]{}, invalidField("seq", "seq is required.")
				}
				return svc.ThreadBranch(ctx, asstapp.BranchInput{ThreadID: in.ThreadID, Seq: *in.Seq})
			}),
		apiCommand(p, asstapp.DescribeThreadDelete(), bodiless(application.APIShape{Area: "threads", Success: 204, Errors: errs(application.NotFound, application.Conflict)}), "DELETE /threads/{thread_id}",
			func(ctx context.Context, svc *asstapp.Service, in apiThreadRequest) (application.Change[asstapp.ThreadChanged], error) {
				return svc.ThreadDelete(ctx, asstapp.ThreadInput{ID: in.ThreadID})
			}),
		apiCommand(p, asstapp.DescribeThreadPin(), threadsArea(application.NotFound, application.Invalid), "PUT /threads/{thread_id}/pin",
			func(ctx context.Context, svc *asstapp.Service, in apiPinRequest) (application.Change[asstapp.ThreadChanged], error) {
				if in.Pinned == nil {
					return application.Change[asstapp.ThreadChanged]{}, invalidField("pinned", "pinned is required.")
				}
				return svc.ThreadPin(ctx, asstapp.FlagInput{ThreadID: in.ThreadID, Set: *in.Pinned})
			}),
		apiCommand(p, asstapp.DescribeThreadArchive(), threadsArea(application.NotFound, application.Invalid), "PUT /threads/{thread_id}/archive",
			func(ctx context.Context, svc *asstapp.Service, in apiArchiveRequest) (application.Change[asstapp.ThreadChanged], error) {
				if in.Archived == nil {
					return application.Change[asstapp.ThreadChanged]{}, invalidField("archived", "archived is required.")
				}
				return svc.ThreadArchive(ctx, asstapp.FlagInput{ThreadID: in.ThreadID, Set: *in.Archived})
			}),
		apiCommand(p, asstapp.DescribeThreadProject(), threadsArea(application.NotFound, application.Conflict), "PUT /threads/{thread_id}/project",
			func(ctx context.Context, svc *asstapp.Service, in apiMoveRequest) (application.Change[asstapp.ThreadChanged], error) {
				move := asstapp.MoveInput{ThreadID: in.ThreadID}
				if in.ProjectID != nil {
					move.ProjectID = *in.ProjectID
				}
				return svc.ThreadProject(ctx, move)
			}),
		apiCommand(p, asstapp.DescribeRunResume(), bodiless(accepted(sends...)), "POST /threads/{thread_id}/runs/{run_id}/resume",
			func(ctx context.Context, svc *asstapp.Service, in apiRunRequest) (application.Change[asstapp.ThreadChanged], error) {
				return svc.RunResume(ctx, asstapp.ResumeInput{ThreadID: in.ThreadID, RunID: in.RunID})
			}),
		apiCommand(p, asstapp.DescribeThreadOptions(), application.APIShape{Area: "composer", Errors: errs(application.NotFound, application.Invalid)}, "PATCH /threads/{thread_id}/options",
			func(ctx context.Context, svc *asstapp.Service, in apiOptionsRequest) (application.Change[asstapp.ThreadChanged], error) {
				opts := asstapp.OptionsInput{ThreadID: in.ThreadID, Model: in.Model, Effort: in.Effort}
				if in.ContextLimit != nil {
					opts.ContextLimit = strconv.FormatInt(*in.ContextLimit, 10)
				}
				return svc.ThreadOptions(ctx, opts)
			}),
		apiQuery(p, asstapp.DescribeConfiguration(), application.APIShape{Area: "composer", ID: "assistant.composer.options", Errors: errs(application.Invalid)}, "GET /composer-options",
			func(ctx context.Context, svc *asstapp.Service, in apiComposerRequest) (application.Read[asstapp.ModelConfiguration], error) {
				return svc.Configuration(ctx, asstapp.ConfigurationInput{Composer: &asstapp.ComposerInput{
					Model: in.Model, Effort: in.Effort, Thinking: in.Thinking, ContextLimit: in.ContextLimit,
					WebSearch: in.WebSearch == nil || *in.WebSearch, CostCap: in.CostCap, Reset: in.Reset,
				}})
			}),
		apiQuery(p, asstapp.DescribeConfiguration(), application.APIShape{Area: "composer"}, "GET /assistant/configuration",
			func(ctx context.Context, svc *asstapp.Service, _ struct{}) (application.Read[asstapp.ModelConfiguration], error) {
				return svc.Configuration(ctx, asstapp.ConfigurationInput{})
			}),
		apiQuery(p, asstapp.DescribeHome(), application.APIShape{Area: "home", Errors: errs(application.Invalid)}, "GET /home",
			func(ctx context.Context, svc *asstapp.Service, in apiHomeRequest) (application.Read[apiHome], error) {
				read, err := svc.Home(ctx, asstapp.HomeInput{Q: in.Q, View: in.View, About: in.About, Limit: config.TuningChatThreadLimit})
				return readAs(read, err, func(h asstapp.Home) apiHome {
					return apiHome{Threads: h.Threads, Q: h.Q, View: h.View, Welcome: h.Welcome, Date: h.Date, Digest: h.Digest,
						Agenda: h.Agenda, About: h.About, Defaults: h.Defaults, Composer: h.Composer}
				})
			}),
		apiQuery(p, asstapp.DescribeHome(), application.APIShape{Area: "home", ID: "assistant.agenda"}, "GET /agenda",
			func(ctx context.Context, svc *asstapp.Service, _ struct{}) (application.Read[apiAgenda], error) {
				read, err := svc.Home(ctx, asstapp.HomeInput{Agenda: true})
				return readAs(read, err, func(h asstapp.Home) apiAgenda { return apiAgenda{Agenda: h.Day} })
			}),
		apiQuery(p, asstapp.DescribeAbout(), application.APIShape{Area: "home"}, "GET /home/about",
			func(ctx context.Context, svc *asstapp.Service, in apiAboutRequest) (application.Read[apiAbout], error) {
				read, err := svc.About(ctx, asstapp.AboutInput{Ref: in.Ref})
				return readAs(read, err, func(a *asstapp.About) apiAbout { return apiAbout{About: a} })
			}),
		apiCommand(p, asstapp.DescribeTodayEnsure(), application.APIShape{Area: "home"}, "POST /today",
			func(ctx context.Context, svc *asstapp.Service, _ struct{}) (application.Change[apiToday], error) {
				change, err := svc.TodayEnsure(ctx, asstapp.TodayInput{})
				return changeAs(change, err, func(t asstapp.TodayResult) apiToday { return apiToday{ThreadID: t.ThreadID} })
			}),
	}
}

func transcriptOut(v asstapp.Transcript, cursor api.Cursor) apiTranscript {
	out := apiTranscript{
		Thread: threadOut(v.Thread), ActiveRun: runOut(v.Active), Queued: []apiQueued{},
		CostUSD: v.Cost, Meter: v.Meter, Items: []apiItem{},
	}
	if v.Earlier != 0 {
		out.NextCursor = cursor.Next(seqCursor{Seq: v.Earlier})
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

func workOut(v asstapp.Work) apiWork {
	out := apiWork{Needs: v.Needs, Plan: v.Plan, Sources: v.Sources, Subagents: v.Subagents, Files: files.Summaries(v.Files), Drafts: []json.RawMessage{}, Code: []json.RawMessage{}}
	for _, d := range v.Drafts {
		out.Drafts = append(out.Drafts, apiRaw(d))
	}
	for _, c := range v.Code {
		out.Code = append(out.Code, apiRaw(c))
	}
	return out
}

func historyOut(v asstapp.History) apiHistory {
	out := apiHistory{Lines: []apiHistoryLine{}, Leaf: v.Leaf}
	for _, l := range v.Lines {
		out.Lines = append(out.Lines, apiHistoryLine(l))
	}
	return out
}
