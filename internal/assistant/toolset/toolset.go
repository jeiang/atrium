// Package toolset is tools/__init__.py's MODULES: the production tool registry, every tool package's Tools()
// in one registry. A tool slice adds its package here; the loop narrows the set per run kind.
package toolset

import (
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/notify"
)

// Registry is the registry every run's tools come from.
func Registry() (*registry.Registry, error) {
	r := registry.New()
	if err := r.Add(notify.Tools()...); err != nil {
		return nil, err
	}
	return r, nil
}
