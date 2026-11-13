package pages

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/web"
)

func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

func (p *pages) readFailed(w http.ResponseWriter, r *http.Request, err error) {
	web.Fail(w, r, err, func(status int) { p.fail(w, r, status, application.Classify(err).Error()) })
}

func (p *pages) loadThread(w http.ResponseWriter, r *http.Request, threadID int64) (*asstapp.Transcript, bool) {
	read, err := p.assistant().Transcript(r.Context(), asstapp.TranscriptInput{ID: threadID})
	if err != nil {
		p.readFailed(w, r, err)
		return nil, false
	}
	return &read.Data, true
}

func (p *pages) loadRun(w http.ResponseWriter, r *http.Request, threadID, runID int64) (*journal.Run, bool) {
	read, err := p.assistant().RunDetail(r.Context(), asstapp.RunInput{ThreadID: threadID, RunID: runID})
	if err != nil {
		p.readFailed(w, r, err)
		return nil, false
	}
	return &read.Data, true
}

func threadOf(view *asstapp.Transcript) threads.Thread { return view.Thread }

func itemsOf(view *asstapp.Transcript) []*service.Item { return view.Items }

func seqOf(item *service.Item) int64 { return item.Seq }

func runOf(item *service.Item) int64 {
	if item.RunID != nil {
		return *item.RunID
	}
	return 0
}

func (p *pages) threadContext(w http.ResponseWriter, r *http.Request, threadID int64) (*ThreadPage, bool) {
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return nil, false
	}
	t := threadOf(view)
	return &ThreadPage{View: view, ThreadID: threadID, History: view.History,
		Composer: composerOf(r.Context(), t, threadID)}, true
}

func (p *pages) transcript(w http.ResponseWriter, r *http.Request, threadID int64, reswap string) {
	data, ok := p.threadContext(w, r, threadID)
	if !ok {
		return
	}
	if reswap != "" {
		w.Header().Set("HX-Reswap", reswap)
	}
	p.fragment(w, r, http.StatusOK, "_transcript.gohtml", data)
}

func (p *pages) chatThread(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	data, ok := p.threadContext(w, r, threadID)
	if !ok {
		return
	}
	ctx := r.Context()
	work, err := p.assistant().Work(ctx, asstapp.ThreadInput{ID: threadID})
	check(err)
	list, err := p.assistant().ProjectList(ctx, asstapp.ProjectListInput{})
	check(err)
	data.Work, data.Projects, data.Project = work.Data, list.Data, nil
	if t := threadOf(data.View); t.ProjectID != nil && *t.ProjectID != 0 {
		found, err := p.assistant().ProjectDetail(ctx, asstapp.ProjectInput{ID: *t.ProjectID})
		if application.Classify(err).Code != application.NotFound {
			check(err)
			data.Project = &found.Data.Project
		}
	}
	if web.IsPartial(r) {
		p.fragment(w, r, http.StatusOK, "_thread.gohtml", data)
		return
	}
	p.page(w, r, "thread.gohtml", data)
}

func (p *pages) chatMessage(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	form, uploads, ok := p.readUploads(w, r)
	if !ok {
		return
	}
	text := strings.TrimSpace(web.FormString(form, "text", ""))
	if text == "" {
		p.failure(w, r, "Nothing sent", emptyMessage)
		return
	}
	ctx := detached(r)
	in := asstapp.SendInput{ThreadID: threadID, Text: text, Files: uploads, Operator: true}
	back := "/chat/" + strconv.FormatInt(threadID, 10)
	if edit, _ := web.Value(form, "edit"); edit != "" {
		_, err := p.assistant().MessageEdit(ctx, in)
		if p.refused(w, r, err) {
			return
		}
		if !web.IsPartial(r) {
			redirect(w, r, back)
			return
		}
		p.transcript(w, r, threadID, "innerHTML")
		return
	}
	change, err := p.assistant().MessageSend(ctx, in)
	if p.refused(w, r, err) {
		return
	}
	sent := change.Data
	if !web.IsPartial(r) {
		redirect(w, r, back)
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	if sent.QueuedID != 0 {
		queued := []asstapp.Queued{}
		for _, q := range view.Queued {
			if q.ID == sent.QueuedID {
				queued = append(queued, q)
			}
		}
		p.fragment(w, r, http.StatusOK, "_queued.gohtml", QueuedData{Queued: queued})
		return
	}
	items := []*service.Item{}
	var after int64
	for _, item := range itemsOf(view) {
		if runOf(item) == sent.RunID {
			items = append(items, item)
			after = max(after, seqOf(item))
		}
	}
	p.fragment(w, r, http.StatusOK, "_run_started.gohtml", RunStarted{View: view, ThreadID: threadID, RunID: sent.RunID, Items: items, After: after})
}

func (p *pages) chatRegenerate(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	if _, err := p.assistant().ReplyRegenerate(detached(r), asstapp.ThreadInput{ID: threadID}); p.refused(w, r, err) {
		return
	}
	if !web.IsPartial(r) {
		redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
		return
	}
	p.transcript(w, r, threadID, "")
}

func (p *pages) chatStop(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	if _, err := p.assistant().RunStop(detached(r), asstapp.ThreadInput{ID: threadID}); p.refused(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *pages) chatBranch(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	form := web.FormValues(r)
	raw, found := web.Value(form, "seq")
	var seq int64
	if !found || raw == "" {
		errs = append(errs, web.Missing("body", "seq"))
	} else if n, ok := numx.LaxInt64(raw); ok {
		seq = n
	} else {
		errs = append(errs, web.BadInt("body", "seq", raw))
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	branched, err := p.assistant().ThreadBranch(detached(r), asstapp.BranchInput{ThreadID: threadID, Seq: seq})
	if errors.As(err, new(service.NothingToRedo)) {
		p.fail(w, r, http.StatusNotFound, err.Error())
		return
	}
	if p.refused(w, r, err) {
		return
	}
	redirect(w, r, "/chat/"+strconv.FormatInt(branched.Data.ThreadID, 10))
}

func (p *pages) chatWork(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	data, ok := p.threadContext(w, r, threadID)
	if !ok {
		return
	}
	work, err := p.assistant().Work(r.Context(), asstapp.ThreadInput{ID: threadID})
	check(err)
	data.Work = work.Data
	p.fragment(w, r, http.StatusOK, "_work_update.gohtml", data)
}

func queryAfter(r *http.Request, errs *[]web.FieldError) int64 {
	raw := r.URL.Query()["after"]
	if len(raw) == 0 {
		return 0
	}
	value := raw[len(raw)-1]
	n, ok := numx.LaxInt64(value)
	if !ok {
		*errs = append(*errs, web.BadInt("query", "after", value))
	}
	return n
}

func (p *pages) chatRun(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	runID, more := pathInt(r, "run_id")
	errs = append(errs, more...)
	after := queryAfter(r, &errs)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	found, ok := p.loadRun(w, r, threadID, runID)
	if !ok {
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	if found.State == journal.Running {
		p.fragment(w, r, http.StatusOK, "_run_live.gohtml", RunLive{View: view, ThreadID: threadID, RunID: runID, After: after})
		return
	}
	items := []*service.Item{}
	for _, item := range itemsOf(view) {
		if seqOf(item) > after && runOf(item) == runID {
			items = append(items, item)
		}
	}
	p.fragment(w, r, http.StatusOK, "_run_done.gohtml", RunDone{View: view, ThreadID: threadID, Run: found, Items: items})
}

func (p *pages) chatResume(w http.ResponseWriter, r *http.Request) {
	threadID, errs := pathInt(r, "thread_id")
	runID, more := pathInt(r, "run_id")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	if _, ok := p.loadRun(w, r, threadID, runID); !ok {
		return
	}
	if _, err := p.assistant().RunResume(detached(r), asstapp.ResumeInput{ThreadID: threadID, RunID: runID}); p.refused(w, r, err) {
		return
	}
	if !web.IsPartial(r) {
		redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
		return
	}
	view, ok := p.loadThread(w, r, threadID)
	if !ok {
		return
	}
	var last int64
	for _, item := range itemsOf(view) {
		last = max(last, seqOf(item))
	}
	p.fragment(w, r, http.StatusOK, "_run_live.gohtml", RunLive{View: view, ThreadID: threadID, RunID: runID, After: last})
}

func (p *pages) chatAnswer(w http.ResponseWriter, r *http.Request) {
	threadID, ok := pathID(w, r, "thread_id")
	if !ok {
		return
	}
	form := web.FormValues(r)
	text := web.FormString(form, "text", "")
	if strings.TrimSpace(text) == "" {
		web.Unprocessable(w, web.FieldError{Loc: []string{"body", "text"}, Msg: "Type an answer first."})
		return
	}
	if _, err := p.assistant().RunAnswer(detached(r), asstapp.AnswerInput{ThreadID: threadID, Text: text}); p.refused(w, r, err) {
		return
	}
	if !web.IsPartial(r) {
		redirect(w, r, "/chat/"+strconv.FormatInt(threadID, 10))
		return
	}
	p.transcript(w, r, threadID, "")
}

func composerOf(ctx context.Context, t threads.Thread, live int64) asstapp.ComposerView {
	return asstapp.Composer(ctx, asstapp.ComposerState{Model: t.Model, Effort: t.Effort, Thinking: t.Thinking,
		ContextLimit: threads.ContextLimitOf(t), WebSearch: bool(t.WebSearch), CostCap: t.CostCapUSD, Live: live})
}
