package reviewpages

import (
	"net/http"
	"strconv"

	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

var snoozeChoices = []int{1, 3, 7, 14}

const loopLimit = 100

type loopsPage struct {
	Owe           []openloops.Loop       `json:"owe"`
	Waiting       []openloops.Loop       `json:"waiting"`
	Commitments   []openloops.Commitment `json:"commitments"`
	SnoozeChoices []int                  `json:"snooze_choices"`
}

func (s *service) showLoops(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	ctx := r.Context()
	now := s.now()
	owe, err := openloops.ListLoops(ctx, s.deps.DB.Read, now, openloops.OweReply, loopLimit, false)
	check(err)
	waiting, err := openloops.ListLoops(ctx, s.deps.DB.Read, now, openloops.Waiting, loopLimit, false)
	check(err)
	commitments, err := openloops.ListCommitments(ctx, s.deps.DB.Read, openloops.Open, loopLimit)
	check(err)
	s.page(w, r, http.StatusOK, "loops", "openloops.gohtml", loopsPage{Owe: owe, Waiting: waiting, Commitments: commitments, SnoozeChoices: snoozeChoices}, notice)
}

func (s *service) notFound(w http.ResponseWriter, r *http.Request, found bool, what string) bool {
	if !found {
		s.fail(w, r, http.StatusNotFound, "unknown "+what)
	}
	return found
}

func (s *service) loopsPage(w http.ResponseWriter, r *http.Request) { s.showLoops(w, r, nil) }

func plural(n int64, noun string) string {
	if n == 1 {
		return strconv.FormatInt(n, 10) + " " + noun
	}
	return strconv.FormatInt(n, 10) + " " + noun + "s"
}

func (s *service) loopSnooze(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "loop_id")
	if !ok {
		return
	}
	days := int64(openloops.SnoozeDefaultDays)
	if raw, given := web.FormField(formValues(r), "days"); given {
		n, valid := py.LaxInt64(raw)
		if !valid {
			web.Unprocessable(w, web.BadInt("body", "days", raw))
			return
		}
		days = n
	}
	days = max(1, min(days, 60))
	found, err := openloops.Snooze(r.Context(), s.deps.DB.Write, id, s.now()+float64(days)*openloops.Day)
	check(err)
	if !s.notFound(w, r, found, "loop") {
		return
	}
	s.showLoops(w, r, toast("success", "Snoozed", "Hidden for "+plural(days, "day")+"."))
}

func (s *service) loopDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "loop_id")
	if !ok {
		return
	}
	found, err := openloops.Dismiss(r.Context(), s.deps.DB.Write, id)
	check(err)
	if !s.notFound(w, r, found, "loop") {
		return
	}
	s.showLoops(w, r, toast("success", "Dismissed"))
}

func (s *service) loopDismissAll(w http.ResponseWriter, r *http.Request) {
	state, given := web.FormField(formValues(r), "state")
	if !given {
		web.Unprocessable(w, web.Missing("body", "state"))
		return
	}
	if state != openloops.OweReply && state != openloops.Waiting {
		s.fail(w, r, http.StatusBadRequest, "unknown state")
		return
	}
	count, err := openloops.DismissAll(r.Context(), s.deps.DB.Write, state)
	check(err)
	s.showLoops(w, r, toast("success", "Dismissed", plural(count, "loop")+" cleared."))
}

func (s *service) commitmentUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "commitment_id")
	if !ok {
		return
	}
	var status, title string
	switch web.PathParam(r, "outcome") {
	case "done":
		status, title = openloops.Done, "Marked done"
	case "dismiss":
		status, title = openloops.Dismissed, "Dismissed"
	default:
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	found, err := openloops.SetCommitmentStatus(r.Context(), s.deps.DB.Write, id, status)
	check(err)
	if !s.notFound(w, r, found, "commitment") {
		return
	}
	s.showLoops(w, r, toast("success", title))
}
