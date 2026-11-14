package pages

import (
	"errors"
	"net/http"
	"strings"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/web"
)

const checkRows = 20

func (s *service) triagePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var offset int
	if _, given := q["offset"]; given {
		raw := web.ValueOr(q, "offset", "")
		n, ok := laxInt64(raw)
		if !ok {
			web.Unprocessable(w, web.BadInt("query", "offset", raw))
			return
		}
		offset = int(max(n, 0))
	}
	account, stage, agreement := web.QueryValue(q, "account"), web.QueryValue(q, "stage"), web.QueryValue(q, "agreement")
	text := strings.TrimSpace(web.QueryValue(q, "q"))
	ctx := r.Context()
	read, err := s.reads.Triage(ctx, mailapp.TriageInput{Account: account, Stage: stage, Agreement: agreement, Q: text, Offset: offset, Full: !web.IsPartial(r)})
	var unknown mailapp.TriageFilterError
	if errors.As(err, &unknown) {
		s.fail(w, r, http.StatusUnprocessableEntity, unknown.Error())
		return
	}
	check(err)
	if web.FragmentNotModified(w, r, read.Revision) {
		return
	}
	data := read.Data
	if web.IsPartial(r) {
		name := "_decisions.gohtml"
		if offset > 0 {
			name = "_triage_rows.gohtml"
		}
		s.fragment(w, r, name, data, nil)
		return
	}
	s.page(w, r, http.StatusOK, "triage", "triage.gohtml", data, nil)
}
