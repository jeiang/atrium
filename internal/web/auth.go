package web

import (
	"errors"
	"fmt"
)

type AuthSet struct {
	Proxy, Local, OIDC bool
}

func ParseAuth(modes []string) (AuthSet, error) {
	if len(modes) == 0 {
		return AuthSet{Proxy: true}, nil
	}
	var set AuthSet
	for _, mode := range modes {
		switch mode {
		case AuthProxy:
			set.Proxy = true
		case AuthLocal:
			set.Local = true
		case AuthOIDC:
			set.OIDC = true
		default:
			return AuthSet{}, fmt.Errorf("unknown auth mode %q", mode)
		}
	}
	if set.Local && (set.Proxy || set.OIDC) {
		return AuthSet{}, errors.New("auth mode local cannot combine with another mode")
	}
	return set, nil
}
