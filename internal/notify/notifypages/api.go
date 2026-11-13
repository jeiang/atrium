package notifypages

import (
	"net/http"

	notifyapp "github.com/jeiang/atrium/internal/notify/application"
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
	found, err := h.service.Inbox(r.Context(), notifyapp.InboxInput{Before: cursor.ID, Limit: limit})
	if err != nil {
		panic(err)
	}
	out := web.List[notifyapp.Notification]{Items: []notifyapp.Notification{}}
	if found.Data.More {
		out.NextCursor = web.EncodeCursor(inboxCursor{ID: found.Data.Items[limit-1].ID})
	}
	out.Items = append(out.Items, found.Data.Items...)
	web.JSON(w, http.StatusOK, out)
}
