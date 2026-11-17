package signalsmodule

import (
	"context"
	"embed"
	"io/fs"

	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/review/hub"
	"github.com/jeiang/atrium/internal/signals"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var snoozeChoices = []int{1, 3, 7, 14}

func snoozeChoicesFor(def int) []int {
	choices := append([]int{}, snoozeChoices...)
	if !slices.Contains(choices, def) {
		choices = append(choices, def)
		slices.Sort(choices)
	}
	return choices
}

const loopLimit = 100

//go:embed templates/*.gohtml
var templates embed.FS

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type pages struct {
	svc  *Service
	deps *web.Deps
}

func newService(d *web.Deps) *Service {
	effects, err := module.SetFrom(d.Ctx).Catalog().Ledger(d.DB, d.Env, d.Cfg, WallClock)
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(effects.Drain)
	return New(d.DB, WallClock, commitmentsapp.New(d.DB, effects, WallClock))
}

func pageRoutes(r chi.Router, d *web.Deps, svc *Service) {
	s := &pages{svc: svc, deps: d}
	r = r.With(bindSignals)
	r.Get("/loops", s.loopsPage)
	r.Post("/loops/{loop_id}/snooze", s.loopSnooze)
	r.Post("/loops/{loop_id}/dismiss", s.loopDismiss)
	r.Post("/loops/dismiss-all", s.loopDismissAll)
	r.Post("/loops/commitments/{commitment_id}/{outcome}", s.commitmentUpdate)
}

func bindSignals(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		ctx, err := module.BindOwner(r.Context(), unit.Signals)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *pages) modules(ctx context.Context) context.Context {
	if module.SetFrom(ctx) == nil {
		return module.WithSet(ctx, module.SetFrom(s.deps.Ctx))
	}
	return ctx
}

func (s *pages) page(w http.ResponseWriter, r *http.Request, status int, page, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, page, data)
	hub.Check(err)
	f.Toast = notice
	hub.Check(s.deps.Render.Page(w, r, status, name, f))
}

func (s *pages) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	s.deps.Error(w, r, status, detail)
}

type loopsPage struct {
	Owe           []signals.Loop       `json:"owe"`
	Waiting       []signals.Loop       `json:"waiting"`
	Commitments   []signals.Commitment `json:"commitments"`
	SnoozeChoices []int                `json:"snooze_choices"`
	SnoozeDefault int                  `json:"snooze_default"`
}

func (s *pages) showLoops(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	ctx := r.Context()
	owe, err := s.svc.OpenLoops(ctx, OpenLoopsInput{State: signals.OweReply, Limit: loopLimit, Snooze: true})
	hub.Check(err)
	waiting, err := s.svc.OpenLoops(ctx, OpenLoopsInput{State: signals.Waiting, Limit: loopLimit})
	hub.Check(err)
	commitments, err := s.svc.Commitments(ctx, CommitmentsInput{Status: signals.Open, Limit: loopLimit})
	hub.Check(err)
	snoozeDefault := owe.Data.SnoozeDefault
	choices := snoozeChoicesFor(snoozeDefault)
	s.page(w, r, http.StatusOK, "loops", "openloops.gohtml", loopsPage{Owe: owe.Data.Loops, Waiting: waiting.Data.Loops, Commitments: commitments.Data.Commitments, SnoozeChoices: choices, SnoozeDefault: snoozeDefault}, notice)
}

func (s *pages) changed(w http.ResponseWriter, r *http.Request, err error) bool {
	if e, ok := errors.AsType[*application.Error](err); ok {
		switch e.Code {
		case application.NotFound:
			s.fail(w, r, http.StatusNotFound, e.Message)
			return false
		case application.Invalid:
			s.fail(w, r, http.StatusBadRequest, e.Message)
			return false
		}
	}
	hub.Check(err)
	return true
}

func (s *pages) loopsPage(w http.ResponseWriter, r *http.Request) { s.showLoops(w, r, nil) }

func plural(n int64, noun string) string {
	if n == 1 {
		return strconv.FormatInt(n, 10) + " " + noun
	}
	return strconv.FormatInt(n, 10) + " " + noun + "s"
}

func (s *pages) loopSnooze(w http.ResponseWriter, r *http.Request) {
	id, errs := hub.PathInt(r, "loop_id")
	var days *int64
	if raw, given := web.Value(web.FormValues(r), "days"); given && raw != "" {
		n, valid := ClampDays(raw)
		if !valid {
			errs = append(errs, web.BadInt("body", "days", raw))
		} else {
			days = &n
		}
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	done, err := s.svc.LoopSnooze(r.Context(), LoopSnoozeInput{ID: id, Days: days})
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, hub.Toast("success", "Snoozed", "Hidden for "+plural(done.Data.Days, "day")+"."))
}

func (s *pages) loopDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := hub.PathID(w, r, "loop_id")
	if !ok {
		return
	}
	_, err := s.svc.LoopDismiss(r.Context(), LoopInput{ID: id})
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, hub.Toast("success", "Dismissed"))
}

func (s *pages) loopDismissAll(w http.ResponseWriter, r *http.Request) {
	state, given := web.Value(web.FormValues(r), "state")
	if !given || state == "" {
		web.Unprocessable(w, web.Missing("body", "state"))
		return
	}
	done, err := s.svc.LoopsDismissMatching(r.Context(), LoopsDismissMatchingInput{State: state})
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, hub.Toast("success", "Dismissed", plural(done.Data.Count, "loop")+" cleared."))
}

func (s *pages) commitmentUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := hub.PathID(w, r, "commitment_id")
	if !ok {
		return
	}
	var err error
	var title string
	switch web.PathParam(r, "outcome") {
	case "done":
		title = "Marked done"
		_, err = s.svc.CommitmentDone(s.modules(r.Context()), CommitmentInput{ID: id})
	case "dismiss":
		title = "Dismissed"
		_, err = s.svc.CommitmentDismiss(s.modules(r.Context()), CommitmentInput{ID: id})
	default:
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, hub.Toast("success", title))
}
