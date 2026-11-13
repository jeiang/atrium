//go:build linux || darwin

package mediaagent

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	problem(w, 405, protocol.CodeMethodNotAllowed, "the method is not allowed")
	return false
}
func (a *Agent) filingRoutes(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/v1/source":
		if requireMethod(w, r, http.MethodPost) {
			a.serveSource(w, r)
		}
	case "/v1/entries":
		if requireMethod(w, r, http.MethodPost) {
			a.serveEntries(w, r)
		}
	case "/v1/imports":
		if allowRead(w, r) {
			a.serveImports(w, r)
		}
	case "/v1/filing/prepare":
		if requireMethod(w, r, http.MethodPost) {
			a.servePrepare(w, r)
		}
	default:
		if !strings.HasPrefix(r.URL.Path, "/v1/filings/") {
			return false
		}
		a.serveFiling(w, r)
	}
	return true
}
func filingProblem(w http.ResponseWriter, err error) {
	var p *protocol.Problem
	if errors.As(err, &p) {
		status := 409
		switch p.Code {
		case protocol.CodeInvalidRequest:
			status = 400
		case protocol.CodeNotFound:
			status = 404
		case protocol.CodeRequestTooLarge:
			status = 413
		case protocol.CodeUnsupportedFile:
			status = 422
		case protocol.CodeDraining, protocol.CodeUnavailable:
			status = 503
		}
		writeJSON(w, status, p)
		return
	}
	fileProblem(w, err)
}
func (a *Agent) serveFiling(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/v1/filings/")
	key, inspect := strings.CutSuffix(tail, "/inspect")
	if !keyValid(key) {
		problem(w, 400, protocol.CodeInvalidRequest, "the receipt key is invalid")
		return
	}
	if inspect {
		if requireMethod(w, r, http.MethodPost) {
			a.inspectFiling(w, key)
		}
		return
	}
	switch r.Method {
	case http.MethodPut:
		a.putFiling(w, r, key)
	case http.MethodGet:
		a.getFiling(w, r, key)
	default:
		w.Header().Set("Allow", "GET, PUT")
		problem(w, 405, protocol.CodeMethodNotAllowed, "the method is not allowed")
	}
}
func (a *Agent) putFiling(w http.ResponseWriter, r *http.Request, key string) {
	var req protocol.FilingRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if req.Key != key {
		problem(w, 400, protocol.CodeInvalidRequest, "the URL and body keys must match")
		return
	}
	if req.UndoOf != "" {
		if !keyValid(req.UndoOf) || key != protocol.UndoKey(req.UndoOf) || req.PlanDigest != "" || req.Plan.Version != 0 {
			problem(w, 400, protocol.CodeInvalidRequest, "the undo request is invalid")
			return
		}
		p, err := a.inverse(req.UndoOf)
		if err != nil {
			filingProblem(w, err)
			return
		}
		req.Plan = p
		req.PlanDigest = protocol.CanonicalDigest(p)
	} else if protocol.CanonicalDigest(req.Plan) != req.PlanDigest {
		problem(w, 400, protocol.CodeInvalidRequest, "the plan digest does not match")
		return
	}
	a.receiptMu.Lock()
	existing := a.receipts[key]
	a.receiptMu.Unlock()
	if existing == nil {
		if err := a.validatePlan(req.Plan, req.UndoOf != ""); err != nil {
			filingProblem(w, err)
			return
		}
	}
	rec, err := a.admit(req)
	if err != nil {
		var p *protocol.Problem
		if errors.As(err, &p) {
			filingProblem(w, err)
			return
		}
		problem(w, 503, protocol.CodeUnavailable, "the receipt could not be made durable")
		return
	}
	rec.mu.Lock()
	if !rec.started {
		rec.started = true
		a.filings.Add(1)
		go func() { defer a.filings.Done(); a.execute(rec) }()
	}
	rec.mu.Unlock()
	for {
		result, changed := rec.snapshot()
		if result.State != "running" {
			writeJSON(w, 200, result)
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}
func (a *Agent) getFiling(w http.ResponseWriter, r *http.Request, key string) {
	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil {
		problem(w, 400, protocol.CodeInvalidRequest, "the query is malformed")
		return
	}
	for key, values := range q {
		if (key != "after" && key != "wait_seconds") || len(values) != 1 {
			problem(w, 400, protocol.CodeInvalidRequest, "the progress query is invalid")
			return
		}
	}
	after, err := strconv.Atoi(q.Get("after"))
	if q.Get("after") == "" {
		after = -1
		err = nil
	}
	wait, e := strconv.Atoi(q.Get("wait_seconds"))
	if q.Get("wait_seconds") == "" {
		wait = 0
		e = nil
	}
	if err != nil || e != nil || after < -1 || wait < 0 || wait > 20 {
		problem(w, 400, protocol.CodeInvalidRequest, "the progress query is invalid")
		return
	}
	a.receiptMu.Lock()
	rec := a.receipts[key]
	a.receiptMu.Unlock()
	if rec == nil {
		problem(w, 404, protocol.CodeNotFound, "the receipt does not exist")
		return
	}
	timer := time.NewTimer(time.Duration(wait) * time.Second)
	defer timer.Stop()
	for {
		result, changed := rec.snapshot()
		if result.State != "running" || len(result.Steps) > after || wait == 0 {
			writeJSON(w, 200, result)
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			result, _ = rec.snapshot()
			writeJSON(w, 200, result)
			return
		case <-r.Context().Done():
			return
		}
	}
}
func (a *Agent) inspectFiling(w http.ResponseWriter, key string) {
	a.receiptMu.Lock()
	rec := a.receipts[key]
	a.receiptMu.Unlock()
	if rec == nil {
		problem(w, 404, protocol.CodeNotFound, "the receipt does not exist")
		return
	}
	result, _ := rec.snapshot()
	out := protocol.Inspection{Result: result, Evidence: []protocol.Evidence{}}
	for _, s := range rec.request.Plan.Steps {
		e := protocol.Evidence{Index: s.Index}
		if s.Source != nil {
			st, err := a.roots.stat(s.Source.Path)
			if err == nil {
				id := identity(st)
				e.Source = &id
			} else {
				e.ErrorCode = codeOf(err)
			}
		}
		st, err := a.roots.stat(s.Destination)
		if err == nil {
			id := identity(st)
			e.Destination = &id
		} else {
			e.ErrorCode = codeOf(err)
		}
		out.Evidence = append(out.Evidence, e)
	}
	writeJSON(w, 200, out)
}
