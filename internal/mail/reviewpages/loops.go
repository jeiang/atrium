package reviewpages

import (
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/mail/openloops"
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
	Owe           []openloops.Loop       `json:"owe"`
	Waiting       []openloops.Loop       `json:"waiting"`
	Commitments   []openloops.Commitment `json:"commitments"`
	SnoozeChoices []int                  `json:"snooze_choices"`
	SnoozeDefault int                    `json:"snooze_default"`
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
	snoozeDefault, err := openloops.SnoozeDefault(ctx, s.deps.DB.Read)
	check(err)
	choices := snoozeChoicesFor(snoozeDefault)
	s.page(w, r, http.StatusOK, "loops", "openloops.gohtml", loopsPage{Owe: owe, Waiting: waiting, Commitments: commitments, SnoozeChoices: choices, SnoozeDefault: snoozeDefault}, notice)
}

func (s *service) notFound(w http.ResponseWriter, r *http.Request, found bool, what string) bool {
	if !found {
		s.fail(w, r, http.StatusNotFound, "unknown "+what)
	}
	return found
}

func (s *service) loopsPage(w http.ResponseWriter, r *http.Request) { s.showLoops(w, r, nil) }

func clampDays(raw string) (int64, bool) {
	body := strings.TrimSpace(raw)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return 0, false
	}
	if whole == "" {
		return 0, false
	}
	n, ok := new(big.Int).SetString(strings.ReplaceAll(whole, "_", ""), 10)
	if !ok {
		return 0, false
	}
	if neg {
		n.Neg(n)
	}
	switch {
	case n.Cmp(big.NewInt(1)) < 0:
		return 1, true
	case n.Cmp(big.NewInt(60)) > 0:
		return 60, true
	}
	return n.Int64(), true
}

func plural(n int64, noun string) string {
	if n == 1 {
		return strconv.FormatInt(n, 10) + " " + noun
	}
	return strconv.FormatInt(n, 10) + " " + noun + "s"
}

func (s *service) loopSnooze(w http.ResponseWriter, r *http.Request) {
	id, errs := pathInt(r, "loop_id")
	snooze, err := openloops.SnoozeDefault(r.Context(), s.deps.DB.Read)
	check(err)
	days := int64(snooze)
	if raw, given := web.Value(web.FormValues(r), "days"); given && raw != "" {
		n, valid := clampDays(raw)
		if !valid {
			errs = append(errs, web.BadInt("body", "days", raw))
		} else {
			days = n
		}
	}
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return
	}
	found, err := openloops.Snooze(r.Context(), s.deps.DB.Write, id, s.now()+float64(days)*openloops.Day, s.now())
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
	found, err := openloops.Dismiss(r.Context(), s.deps.DB.Write, id, s.now())
	check(err)
	if !s.notFound(w, r, found, "loop") {
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
	if state != openloops.OweReply && state != openloops.Waiting {
		s.fail(w, r, http.StatusBadRequest, "unknown state")
		return
	}
	count, err := openloops.DismissAll(r.Context(), s.deps.DB.Write, state, s.now())
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
