package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/webusage"
)

const (
	usageFlushEvery   = time.Minute
	usageFlushTimeout = 10 * time.Second
)

func (s *Server) startUsage(o Options) {
	if o.DB == nil {
		return
	}
	s.usage = webusage.NewBatch()
	s.usageDB = o.DB
	s.usageLoc = time.UTC
	if o.State != nil {
		loc, err := o.State.Location(o.Env)
		if err != nil {
			log.Warn("usage counters fall back to UTC days", logx.Err, err.Error())
		} else {
			s.usageLoc = loc
		}
	}
	s.usageLoop = make(chan struct{})
	go func() {
		defer close(s.usageLoop)
		tick := time.NewTicker(usageFlushEvery)
		defer tick.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-tick.C:
				s.flushUsage()
			}
		}
	}()
}

func (s *Server) recordUsage(r *http.Request, route string) {
	if s.usage == nil || r.URL.Path == healthPath || strings.HasPrefix(r.URL.Path, staticPrefix) {
		return
	}
	who, _ := IdentityOf(r.Context())
	surface := webusage.SurfacePage
	if sf, ok := surfaceOf(r); ok {
		surface = strings.TrimPrefix(sf.scope, "/")
	}
	method := r.Method
	if !slices.Contains(methods[:], method) {
		method = "OTHER"
	}
	s.usage.Add(webusage.Key{Day: time.Now().In(s.usageLoc).Format(time.DateOnly), Surface: surface, Method: method, Route: route, Agent: who.Agent || markedAgent(r)})
}

func (s *Server) flushUsage() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.base), usageFlushTimeout)
	defer cancel()
	if err := s.usage.Flush(ctx, s.usageDB, time.Now().In(s.usageLoc)); err != nil {
		log.Error("usage counters were not saved; they stay batched for the next flush", logx.Err, err.Error())
	}
}

func (s *Server) closeUsage() {
	if s.usage == nil {
		return
	}
	<-s.usageLoop
	s.flushUsage()
}
