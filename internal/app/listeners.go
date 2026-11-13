package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/web"
)

type listener struct {
	name     string
	net      net.Listener
	srv      *http.Server
	done     chan error
	handlers sync.WaitGroup
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
	return &listener{name: name, net: l, done: make(chan error, 1)}, nil
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

func (l *listener) serve(failed chan<- error) {
	next := l.srv.Handler
	l.srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.handlers.Add(1)
		defer l.handlers.Done()
		next.ServeHTTP(w, r)
	})
	err := l.srv.Serve(l.net)
	if !errors.Is(err, http.ErrServerClosed) {
		err = fmt.Errorf("%s listener stopped: %w", l.name, err)
		failed <- err
	}
	l.done <- err
}

func (l *listener) shutdown(sd *shutdown) error {
	var err error
	if serr := l.srv.Shutdown(sd.work); serr != nil {
		log.Warn(fmt.Sprintf("%s listener requests outlived the work deadline; closing their connections", l.name), logx.Err, serr.Error())
		err = l.srv.Close()
		if werr := wait(sd.settle, &l.handlers); werr != nil {
			err = errors.Join(err, fmt.Errorf("%s listener: requests did not finish before the shutdown deadline", l.name))
		}
	}
	if serveErr := <-l.done; !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
