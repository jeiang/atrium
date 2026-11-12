package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/jeiang/atrium/internal/web"
)

type listener struct {
	name string
	net  net.Listener
	srv  *http.Server
}

type listeners struct {
	public, emergency, ops *listener
}

func bind(plan web.Plan, opsAddr string) (*listeners, error) {
	ls := &listeners{}
	var err error
	if ls.public, err = listen("public", plan.Public.Addr); err != nil {
		return nil, err
	}
	if plan.Emergency != nil {
		if ls.emergency, err = listen("emergency", plan.Emergency.Addr); err != nil {
			return nil, errors.Join(err, ls.close())
		}
	}
	if opsAddr != "" {
		if ls.ops, err = listen("ops", opsAddr); err != nil {
			return nil, errors.Join(err, ls.close())
		}
	}
	return ls, nil
}

func listen(name, addr string) (*listener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%s listener: %w", name, err)
	}
	return &listener{name: name, net: l}, nil
}

func (ls *listeners) all() []*listener {
	var out []*listener
	for _, l := range []*listener{ls.public, ls.emergency, ls.ops} {
		if l != nil {
			out = append(out, l)
		}
	}
	return out
}

func (ls *listeners) close() error {
	var err error
	for _, l := range ls.all() {
		if l.srv == nil {
			if cerr := l.net.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
				err = errors.Join(err, cerr)
			}
		}
	}
	return err
}

func (l *listener) serve(results chan<- error) {
	err := l.srv.Serve(l.net)
	if !errors.Is(err, http.ErrServerClosed) {
		err = fmt.Errorf("%s listener stopped: %w", l.name, err)
	}
	results <- err
}
