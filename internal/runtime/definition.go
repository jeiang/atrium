package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Backoff struct {
	Initial    time.Duration
	Maximum    time.Duration
	ResetAfter time.Duration
}

var DefaultBackoff = Backoff{Initial: time.Second, Maximum: time.Minute, ResetAfter: 5 * time.Minute}

type ServiceDefinition struct {
	Name     string
	Critical bool
	Backoff  Backoff
	Stall    time.Duration
	Run      func(context.Context, *Services) error
}

func (b Backoff) orDefault() Backoff {
	if b == (Backoff{}) {
		return DefaultBackoff
	}
	return b
}

func (b Backoff) validate() error {
	if b.Initial <= 0 || b.Maximum < b.Initial || b.ResetAfter <= 0 {
		return errors.New("the backoff needs a positive initial delay, a maximum no smaller than it and a positive reset period")
	}
	return nil
}

func Validate(defs []ServiceDefinition) error {
	_, err := validate(defs)
	return err
}

func validate(defs []ServiceDefinition) ([]ServiceDefinition, error) {
	out := make([]ServiceDefinition, 0, len(defs))
	seen := map[string]bool{}
	for _, def := range defs {
		if def.Name == "" {
			return nil, errors.New("runtime: a service has no name")
		}
		if seen[def.Name] {
			return nil, fmt.Errorf("runtime: duplicate service %q", def.Name)
		}
		seen[def.Name] = true
		if def.Run == nil {
			return nil, fmt.Errorf("runtime: service %q has no run function", def.Name)
		}
		if def.Stall < 0 {
			return nil, fmt.Errorf("runtime: service %q has a negative stall threshold", def.Name)
		}
		def.Backoff = def.Backoff.orDefault()
		if err := def.Backoff.validate(); err != nil {
			return nil, fmt.Errorf("runtime: service %q: %w", def.Name, err)
		}
		out = append(out, def)
	}
	return out, nil
}
