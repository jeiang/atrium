package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"
)

const (
	Shadow = "shadow"
	Live   = "live"
)

var Modes = [...]string{Shadow, Live}

type Env func(name string) (string, bool)

var OS Env = os.LookupEnv

func Map(m map[string]string) Env {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

func get(env Env, name string) string {
	v, _ := env(name)
	return v
}

func stripped(env Env, name string) string { return strings.TrimSpace(get(env, name)) }

func Zone(name string) (*time.Location, error) {
	if name != "" && name != "Local" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, nil
		}
	}
	return nil, fmt.Errorf("unknown timezone %s", strconv.Quote(name))
}

func (s *State) Location(env Env) (*time.Location, error) { return Zone(s.TZName(env)) }

type CalDAVLogin struct {
	AppleID  string
	Password string
}
