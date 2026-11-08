package notify

import (
	"net/http"

	"github.com/jeiang/atrium/internal/web"
)

type inboxCursor struct {
	ID int64 `json:"id"`
}

func (h handlers) apiInbox(w http.ResponseWriter, r *http.Request) {
	var cursor inboxCursor
	limit, after, ok := web.Paging(w, r, &cursor)
	if !ok {
		return
	}
	if !after {
		cursor.ID = 0
	}
	found, err := RecentBefore(r.Context(), h.d.DB.Read, cursor.ID, limit+1)
	if err != nil {
		panic(err)
	}
	out := web.List[Notification]{Items: []Notification{}}
	if len(found) > limit {
		found = found[:limit]
		out.NextCursor = web.EncodeCursor(inboxCursor{ID: found[limit-1].ID})
	}
	out.Items = append(out.Items, found...)
	web.JSON(w, http.StatusOK, out)
}
