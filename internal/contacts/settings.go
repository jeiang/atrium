package contacts

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	DirKey = "contacts_dir"
	DirEnv = "ATRIUM_CONTACTS_" + DirKey
)

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: DirKey, Module: "contacts", Type: settings.String}))
}

type State uint8

const (
	Off State = iota
	Unset
	Ready
)

type Source struct {
	State State
	Dir   string
}

func Resolve(ctx context.Context, q db.Querier) (Source, error) {
	on, err := settings.From(ctx).Resolve(ctx, q, module.FlagKey(module.Contacts))
	if err != nil {
		return Source{}, err
	}
	if on.Value != "true" {
		return Source{State: Off}, nil
	}
	found, err := settings.From(ctx).Resolve(ctx, q, DirKey)
	if err != nil {
		return Source{}, err
	}
	if found.Value == "" {
		return Source{State: Unset}, nil
	}
	return Source{State: Ready, Dir: filepath.Clean(found.Value)}, nil
}

func Dir(ctx context.Context, q db.Querier) (string, error) {
	src, err := Resolve(ctx, q)
	if err != nil {
		return "", err
	}
	return src.Dir, nil
}

func MaxResults(ctx context.Context, q db.Querier) (int64, error) {
	return TuningContactsMaxResults, nil
}
