package actionlink

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/logx"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
)

func PublicCheck() func(*http.Request) bool {
	return func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/actions/")
	}
}

var log = logx.Logger("atrium.actionlink")

func Handler(phone func(ctx context.Context, token string, now float64) (*proposalsapp.Proposal, error), now func() float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		unfinished, err := phone(r.Context(), chi.URLParam(r, "token"), now())
		switch {
		case errors.Is(err, ErrUnavailable):
			http.Error(w, "action link unavailable", http.StatusInternalServerError)
		case errors.Is(err, ErrUsed):
			http.Error(w, "This link was already used.", http.StatusGone)
		case errors.Is(err, ErrExpired):
			http.Error(w, "This link expired.", http.StatusGone)
		case errors.Is(err, ErrInvalid):
			http.Error(w, "This link is not valid.", http.StatusBadRequest)
		case errors.Is(err, ErrRefused):
			http.Error(w, "This action is not allowed from the phone.", http.StatusForbidden)
		case unfinished == nil && proposalsapp.IsLockBusy(err):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case err != nil && unfinished != nil:
			log.Error("action link did not finish", logx.Err, err.Error())
			text := "The action did not finish: " + err.Error() + ". The proposal is marked failed."
			if unfinished.Undoable {
				text += " Undo what it already did from atrium."
			}
			http.Error(w, text, http.StatusConflict)
		case err != nil:
			log.Error("action link failed", logx.Err, err.Error())
			http.Error(w, "The action failed.", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Done. The proposal was handled from the phone."))
		}
	}
}
