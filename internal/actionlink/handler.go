package actionlink

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/proposals"
)

func PublicCheck(_ config.Config, _ config.Env, _ *db.DB) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/actions/")
	}
}

var log = logx.Logger("atrium.actionlink")

func Handler(pipeline *proposals.Pipeline, now func() float64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		ctx := r.Context()
		pol, err := policy.Load(ctx, pipeline.DB.Read)
		if err != nil {
			http.Error(w, "action link unavailable", http.StatusInternalServerError)
			return
		}
		token := chi.URLParam(r, "token")
		err = Redeem(ctx, pipeline.DB.Write, token, now(), func(ctx context.Context, claims Claims) error {
			prop, err := proposals.Get(ctx, pipeline.DB.Write, claims.ProposalID)
			if err != nil {
				return err
			}
			if prop == nil || prop.Fingerprint != claims.Hash {
				return ErrInvalid
			}
			if !pol.Allows(prop.Kind) {
				return ErrRefused
			}
			who := proposals.Resolver{Actor: "phone", Source: "phone"}
			if claims.Action == Approve {
				_, err := pipeline.ApproveAndApply(ctx, prop.ID, nil, who)
				return err
			}
			return proposals.Dismiss(ctx, pipeline.DB, prop.ID, proposals.Dismissed, who, now())
		})
		switch {
		case errors.Is(err, ErrUsed):
			http.Error(w, "This link was already used.", http.StatusGone)
		case errors.Is(err, ErrExpired):
			http.Error(w, "This link expired.", http.StatusGone)
		case errors.Is(err, ErrInvalid):
			http.Error(w, "This link is not valid.", http.StatusBadRequest)
		case errors.Is(err, ErrRefused):
			http.Error(w, "This action is not allowed from the phone.", http.StatusForbidden)
		case err != nil:
			log.Error("action link failed", logx.Err, err.Error())
			http.Error(w, "The action failed.", http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("Done. The proposal was handled from the phone."))
		}
	}
}
