package contacts

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	DirKey        = "contacts_dir"
	DirEnv        = "ATRIUM_CONTACTS_" + DirKey
	MaxResultsKey = "contacts_max_results"
)

func init() {
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: DirKey, Module: "contacts", Type: settings.String}))
	settings.MustRegister(settings.Value(settings.Advanced, settings.Def{
		Key: MaxResultsKey, Module: "contacts", Type: settings.Int, Default: "10", Min: 1, Max: 100,
		Check: settings.Between(1, 100)}))
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
	on, err := settings.Default.Resolve(ctx, q, module.FlagKey(module.Contacts))
	if err != nil {
		return Source{}, err
	}
	if on.Value != "true" {
		return Source{State: Off}, nil
	}
	found, err := settings.Default.Resolve(ctx, q, DirKey)
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
	found, err := settings.Default.Resolve(ctx, q, MaxResultsKey)
	if err != nil {
		return 0, err
	}
	return found.Int()
}
