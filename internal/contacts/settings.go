package contacts

import (
	"context"
	"path/filepath"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	DirKey = "contacts_dir"
	DirEnv = "ATRIUM_CONTACTS_" + DirKey
)

func HostSettings() []settings.HostDef {
	return []settings.HostDef{
		{Key: DirKey, Module: "contacts", Type: settings.String},
	}
}

func init() { settings.MustRegisterHost(HostSettings()...) }

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
	dir := config.From(ctx).HostValue(DirKey)
	if dir == "" {
		return Source{State: Unset}, nil
	}
	return Source{State: Ready, Dir: filepath.Clean(dir)}, nil
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
