package pages

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
)

func (s *service) triagePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var offset int
	if values, given := q["offset"]; given {
		n, ok := py.LaxInt64(web.LastValue(values, ""))
		if !ok {
			web.Unprocessable(w, web.BadInt("query", "offset", web.LastValue(values, "")))
			return
		}
		offset = int(max(n, 0))
	}
	account, stage, agreement := web.QueryValue(q, "account"), web.QueryValue(q, "stage"), web.QueryValue(q, "agreement")
	text := py.Strip(web.QueryValue(q, "q"))
	ctx := r.Context()
	found, err := triage.Triage(ctx, s.deps.DB.Read, triage.Filter{
		Account: account, Stage: stage, Agreement: agreement, Limit: pageSize, Offset: offset, Q: text,
	})
	var unknown triage.FilterError
	if errors.As(err, &unknown) {
		s.fail(w, r, http.StatusUnprocessableEntity, unknown.Error())
		return
	}
	check(err)
	data := triageData{
		Rows: found.Rows, Total: found.Total, Offset: offset, Account: account, Stage: stage, Agreement: agreement, Q: text,
	}
	if shown := offset + len(found.Rows); shown < found.Total {
		data.NextOffset = shown
	}
	if web.IsPartial(r) {
		name := "_decisions.gohtml"
		if offset > 0 {
			name = "_triage_rows.gohtml"
		}
		s.fragment(w, r, name, data, nil)
		return
	}
	summaries, err := triage.Summaries(ctx, s.deps.DB.Read, s.deps.Env, nil, time.Now())
	check(err)
	data.Summaries = slices.DeleteFunc(summaries, func(s triage.Summary) bool { return account != "" && s.Account != account })
	flags, err := triage.FlagJudgments(ctx, s.deps.DB.Read, account, pageSize+1, 0)
	check(err)
	data.FlagsCapped = len(flags) > pageSize
	data.Flags = flags[:min(len(flags), pageSize)]
	dir, err := contacts.Dir(ctx, s.deps.DB.Read)
	check(err)
	data.NoContacts = dir == ""
	s.page(w, r, http.StatusOK, "triage.gohtml", data, nil)
}
