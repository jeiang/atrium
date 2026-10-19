package reviewpages

import (
	"errors"
	"net/http"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail/birthdays"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const birthdayWindowDays = 60

type watchesPage struct {
	Packages  []watches.Package `json:"packages"`
	Birthdays *[]birthdays.Item `json:"birthdays"`
}

func (s *service) showWatches(w http.ResponseWriter, r *http.Request, notice *tpl.Toast) {
	ctx := r.Context()
	now := s.now()
	packages, err := watches.ListPackages(ctx, s.deps.DB.Read, s.deps.Env, now, true, false)
	check(err)
	zone, err := config.Location(s.deps.Env)
	check(err)
	sec, _ := py.SplitTimestamp(now)
	year, month, day := time.Unix(sec, 0).In(zone).Date()
	items, configured, err := birthdays.UpcomingFor(ctx, s.deps.DB.Read, s.deps.Cfg, birthdays.Date(year, month, day), birthdayWindowDays)
	check(err)
	data := watchesPage{Packages: packages}
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
	key, haveKey := web.FormField(form, "key")
	date, haveDate := web.FormField(form, "date")
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
	if errors.Is(err, httpx.ErrValue) {
		s.fail(w, r, http.StatusUnprocessableEntity, "date must be YYYY-MM-DD")
		return
	}
	check(err)
	s.showWatches(w, r, toast("success", "Dismissed", "Hidden until next year."))
}
