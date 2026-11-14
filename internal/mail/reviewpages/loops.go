package reviewpages

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
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

type loopsPage struct {
	Owe           []mailapp.Loop       `json:"owe"`
	Waiting       []mailapp.Loop       `json:"waiting"`
	Commitments   []mailapp.Commitment `json:"commitments"`
	SnoozeChoices []int                `json:"snooze_choices"`
	SnoozeDefault int                  `json:"snooze_default"`
}

func (s *service) showLoops(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	ctx := r.Context()
	owe, err := s.mail.OpenLoops(ctx, mailapp.OpenLoopsInput{State: mailapp.LoopOweReply, Limit: loopLimit, Snooze: true})
	check(err)
	waiting, err := s.mail.OpenLoops(ctx, mailapp.OpenLoopsInput{State: mailapp.LoopWaiting, Limit: loopLimit})
	check(err)
	commitments, err := s.mail.Commitments(ctx, mailapp.CommitmentsInput{Status: mailapp.CommitmentOpen, Limit: loopLimit})
	check(err)
	snoozeDefault := owe.Data.SnoozeDefault
	choices := snoozeChoicesFor(snoozeDefault)
	s.page(w, r, http.StatusOK, "loops", "openloops.gohtml", loopsPage{Owe: owe.Data.Loops, Waiting: waiting.Data.Loops, Commitments: commitments.Data.Commitments, SnoozeChoices: choices, SnoozeDefault: snoozeDefault}, notice)
}

func (s *service) changed(w http.ResponseWriter, r *http.Request, err error) bool {
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
	check(err)
	return true
}

func (s *service) loopsPage(w http.ResponseWriter, r *http.Request) { s.showLoops(w, r, nil) }

func plural(n int64, noun string) string {
	if n == 1 {
		return strconv.FormatInt(n, 10) + " " + noun
	}
	return strconv.FormatInt(n, 10) + " " + noun + "s"
}

func (s *service) loopSnooze(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "loop_id")
	var days *int64
	if raw, given := web.Value(web.FormValues(r), "days"); given && raw != "" {
		n, valid := mailapp.ClampDays(raw)
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
	done, err := s.mail.LoopSnooze(r.Context(), mailapp.LoopSnoozeInput{ID: id, Days: days})
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, toast("success", "Snoozed", "Hidden for "+plural(done.Data.Days, "day")+"."))
}

func (s *service) loopDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "loop_id")
	if !ok {
		return
	}
	_, err := s.mail.LoopDismiss(r.Context(), mailapp.LoopInput{ID: id})
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, toast("success", "Dismissed"))
}

func (s *service) loopDismissAll(w http.ResponseWriter, r *http.Request) {
	state, given := web.Value(web.FormValues(r), "state")
	if !given || state == "" {
		web.Unprocessable(w, web.Missing("body", "state"))
		return
	}
	done, err := s.mail.LoopsDismissMatching(r.Context(), mailapp.LoopsDismissMatchingInput{State: state})
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, toast("success", "Dismissed", plural(done.Data.Count, "loop")+" cleared."))
}

func (s *service) commitmentUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "commitment_id")
	if !ok {
		return
	}
	var err error
	var title string
	switch web.PathParam(r, "outcome") {
	case "done":
		title = "Marked done"
		_, err = s.mail.CommitmentDone(r.Context(), mailapp.CommitmentInput{ID: id})
	case "dismiss":
		title = "Dismissed"
		_, err = s.mail.CommitmentDismiss(r.Context(), mailapp.CommitmentInput{ID: id})
	default:
		s.fail(w, r, http.StatusNotFound, "unknown action")
		return
	}
	if !s.changed(w, r, err) {
		return
	}
	s.showLoops(w, r, toast("success", title))
}
