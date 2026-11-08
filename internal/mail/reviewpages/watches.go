package reviewpages

import (
	"errors"
	"net/http"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/timex"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type watchesPage struct {
	Packages     []watches.Package `json:"packages"`
	Birthdays    *[]birthdays.Item `json:"birthdays"`
	BirthdayDays int               `json:"birthday_days"`
}

func (s *service) showWatches(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	ctx := r.Context()
	now := s.now()
	packages, err := watches.ListPackages(ctx, s.deps.DB.Read, s.deps.Env, now, true, false)
	check(err)
	zone, err := config.From(ctx).Location(s.deps.Env)
	check(err)
	sec, _ := timex.SplitTimestamp(now)
	year, month, day := time.Unix(sec, 0).In(zone).Date()
	defaultDays, maxDays, err := birthdays.Limits(ctx, s.deps.DB.Read)
	check(err)
	window := min(defaultDays, maxDays)
	if window <= 0 {
		window = birthdays.DefaultDays
	}
	items, configured, err := birthdays.UpcomingFor(ctx, s.deps.DB.Read, birthdays.Date(year, month, day), window)
	check(err)
	data := watchesPage{Packages: packages, BirthdayDays: window}
	if configured {
		if items == nil {
			items = []birthdays.Item{}
		}
		data.Birthdays = &items
	}
	s.page(w, r, http.StatusOK, "watches", "watches.gohtml", data, notice)
}

func (s *service) watchesPage(w http.ResponseWriter, r *http.Request) { s.showWatches(w, r, nil) }

func (s *service) packageDismiss(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "package_id")
	if !ok {
		return
	}
	found, err := watches.DismissPackage(r.Context(), s.deps.DB.Write, id)
	check(err)
	if !s.notFound(w, r, found, "package") {
		return
	}
	s.showWatches(w, r, toast("success", "Dismissed"))
}

func (s *service) birthdayDismiss(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	key, haveKey := web.Value(form, "key")
	date, haveDate := web.Value(form, "date")
	if key == "" {
		haveKey = false
	}
	if date == "" {
		haveDate = false
	}
	var problems []web.FieldError
	if !haveKey {
		problems = append(problems, web.Missing("body", "key"))
	}
	if !haveDate {
		problems = append(problems, web.Missing("body", "date"))
	}
	if len(problems) > 0 {
		web.Unprocessable(w, problems...)
		return
	}
	err := birthdays.Dismiss(r.Context(), s.deps.DB.Write, key, date, s.now())
	if errors.Is(err, httpx.ErrPayload) {
		s.fail(w, r, http.StatusUnprocessableEntity, "date must be YYYY-MM-DD")
		return
	}
	check(err)
	s.showWatches(w, r, toast("success", "Dismissed", "Hidden until next year."))
}
