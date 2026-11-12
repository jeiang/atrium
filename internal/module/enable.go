package module

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

const (
	HoldOff    = "off"
	HoldPinned = "pinned"
	HoldNeeds  = string(NeedsSettings)
	HoldDone   = "enabled"
)

type Hold struct {
	Module string `json:"module"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

func (s *Set) flagged(name string) bool {
	for _, r := range s.decls {
		if r.Name == name {
			return !r.Core
		}
	}
	return false
}

func (s *Set) Held(ctx context.Context, q db.Querier, name string) (Hold, error) {
	if !s.flagged(name) {
		return Hold{}, nil
	}
	h := Hold{Module: name}
	found, err := s.reg.Resolve(ctx, q, FlagKey(name))
	if err != nil || found.Value == "true" {
		return h, err
	}
	source, pinned, err := s.reg.Pinned(FlagKey(name))
	if err != nil {
		return Hold{}, err
	}
	if pinned {
		h.Result, h.Detail = HoldPinned, source
		return h, nil
	}
	err = s.Guard(ctx, q, FlagKey(name), "true")
	var needs *NeedsSettingsError
	if errors.As(err, &needs) {
		h.Result, h.Detail = HoldNeeds, joinReasons(needs.Unmet)
		return h, nil
	}
	if err != nil {
		return Hold{}, err
	}
	h.Result = HoldOff
	return h, nil
}

func (s *Set) Enable(ctx context.Context, d *db.DB, name string) (Hold, error) {
	var h Hold
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if h, err = s.Held(ctx, tx, name); err != nil || h.Result != HoldOff {
			return err
		}
		if err := s.reg.Set(ctx, tx, FlagKey(name), "true"); err != nil {
			return err
		}
		h.Result = HoldDone
		return nil
	})
	return h, err
}
