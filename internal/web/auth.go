package web

import (
	"errors"
	"fmt"
)

type Mode uint8

const (
	ModeProxy Mode = iota + 1
	ModeLocal
	ModeOIDC
	ModeProxyOIDC
)

type Policy struct {
	Mode Mode
	Open bool
}

func (p Policy) Local() bool    { return p.Mode == ModeLocal }
func (p Policy) Proxy() bool    { return p.Mode == ModeProxy || p.Mode == ModeProxyOIDC }
func (p Policy) OIDC() bool     { return p.Mode == ModeOIDC || p.Mode == ModeProxyOIDC }
func (p Policy) Enforced() bool { return !p.Open }

func parseAuth(modes []string) (Policy, error) {
	if len(modes) == 0 {
		return Policy{Mode: ModeProxy}, nil
	}
	var proxy, local, oidc bool
	for _, mode := range modes {
		switch mode {
		case AuthProxy:
			proxy = true
		case AuthLocal:
			local = true
		case AuthOIDC:
			oidc = true
		default:
			return Policy{}, fmt.Errorf("unknown auth mode %q", mode)
		}
	}
	if local && (proxy || oidc) {
		return Policy{}, errors.New("auth mode local cannot combine with another mode")
	}
	switch {
	case local:
		return Policy{Mode: ModeLocal}, nil
	case proxy && oidc:
		return Policy{Mode: ModeProxyOIDC}, nil
	case oidc:
		return Policy{Mode: ModeOIDC}, nil
	default:
		return Policy{Mode: ModeProxy}, nil
	}
}
