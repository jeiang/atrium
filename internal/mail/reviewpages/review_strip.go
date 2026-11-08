package reviewpages

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/jeiang/atrium/internal/db"
)

const stripItems = 3

type stripItem struct {
	URL   string `json:"url"`
	Icon  string `json:"icon"`
	Title string `json:"title"`
	Label string `json:"label"`
}

type stripData struct {
	Items []stripItem `json:"items"`
	Count int         `json:"count"`
}

func (s *service) needsStrip(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.deps.DB.Read.QueryContext(ctx,
		"SELECT id, kind, title FROM proposals WHERE status = 'pending' ORDER BY created_ts DESC, id DESC LIMIT ?", stripItems)
	check(err)
	items, err := db.Collect(rows, func(r *sql.Rows) (stripItem, error) {
		var id int64
		var kind, title string
		if err := r.Scan(&id, &kind, &title); err != nil {
			return stripItem{}, err
		}
		item := stripItem{URL: "/proposals/" + strconv.FormatInt(id, 10), Icon: "list-checks", Title: title, Label: "Proposal"}
		if k, ok := s.registry.Kind(kind); ok {
			item.Label = k.Label
			if k.Icon != "" {
				item.Icon = k.Icon
			}
		}
		return item, nil
	})
	check(err)
	if left := stripItems - len(items); left > 0 {
		rows, err := s.deps.DB.Read.QueryContext(ctx,
			"SELECT id, thread_id, subject FROM drafts WHERE status IN ('draft', 'unknown') "+
				"ORDER BY updated_at DESC, status = 'unknown' DESC, id DESC LIMIT ?", left)
		check(err)
		drafts, err := db.Collect(rows, func(r *sql.Rows) (stripItem, error) {
			var id int64
			var thread sql.NullInt64
			var subject string
			if err := r.Scan(&id, &thread, &subject); err != nil {
				return stripItem{}, err
			}
			item := stripItem{URL: "/review#draft-" + strconv.FormatInt(id, 10), Icon: "mail", Title: subject, Label: "Draft"}
			if thread.Valid {
				item.URL = "/chat/" + strconv.FormatInt(thread.Int64, 10)
			}
			if item.Title == "" {
				item.Title = "(no subject)"
			}
			return item, nil
		})
		check(err)
		items = append(items, drafts...)
	}
	data := stripData{Items: items}
	if len(items) > 0 {
		data.Count, err = needsCount(ctx, s.deps.DB.Read)
		check(err)
	}
	s.fragment(w, r, http.StatusOK, "_review_strip.gohtml", data, nil)
}
