package coremodule

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

const (
	eventsHeartbeat     = 15 * time.Second
	eventsPoll          = 2 * time.Second
	eventsWriteDeadline = 10 * time.Second
)

var topicModules = map[string]string{
	state.Assistant: unit.Assistant,
	state.Knowledge: unit.Assistant,
	state.Tasks:     unit.Assistant,
	state.Mail:      unit.Mail,
	state.Calendar:  unit.Calendar,
	state.Media:     unit.Media,
	state.Notify:    unit.Notifications,
	state.Downloads: unit.Torrents,
}

func eventOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{state.DescribeEvents()}, eventRoutes(nil)
}

func eventRoutes(d *web.Deps) []application.Binding {
	b := application.Binding{Kind: application.API, Target: "GET /events", Uses: []application.Name{state.EventsName}, API: &application.APIShape{
		Area: "home", Request: reflect.TypeFor[state.EventsInput](), Response: reflect.TypeFor[state.AppEvent](),
		Body: application.BodyEventStream, Stream: application.AppStream, Success: http.StatusOK, Errors: []application.Code{application.RateLimited},
	}}
	if d == nil {
		return []application.Binding{api.Handle(nil, b, nil)}
	}
	return []application.Binding{api.Handle(d, b, func(w http.ResponseWriter, r *http.Request) { serveEvents(d, w, r) })}
}

func visibleTopics(ctx context.Context, d *web.Deps, s *state.Store) ([]state.AppEvent, error) {
	counters, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]state.AppEvent, 0, len(counters))
	for _, c := range counters {
		if owner, gated := topicModules[c.Scope]; gated && !module.IsEnabled(d.Ctx, owner) {
			continue
		}
		out = append(out, state.AppEvent{Topic: c.Scope, Revision: c.Value})
	}
	return out, nil
}

func serveEvents(d *web.Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	release, check, err := web.OpenStream(ctx, application.AppStream)
	switch {
	case errors.Is(err, web.ErrStreamLimit):
		web.APIError(w, http.StatusTooManyRequests, "rate_limited", "Too many open streams.")
		return
	case err != nil:
		web.APIError(w, http.StatusUnauthorized, "unauthenticated", "Sign in again.")
		return
	}
	defer release()
	wake, unsubscribe := db.Commits()
	defer unsubscribe()
	store := state.New(d.DB)
	topics, err := visibleTopics(ctx, d, store)
	if err != nil {
		web.APIFail(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "private, no-store, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	write := func(text string) error {
		_ = rc.SetWriteDeadline(time.Now().Add(eventsWriteDeadline))
		if _, err := io.WriteString(w, text); err != nil {
			return err
		}
		return rc.Flush()
	}
	frame := func(event string, data any) string {
		raw, err := json.Marshal(data)
		if err != nil {
			panic(err)
		}
		return "event: " + event + "\ndata: " + string(raw) + "\n\n"
	}
	sent := make(map[string]int64, len(topics))
	for _, t := range topics {
		sent[t.Topic] = t.Revision
	}
	if write("retry: 1000\n\n"+frame("sync", state.AppSync{Topics: topics})) != nil {
		return
	}
	heartbeat := time.NewTicker(eventsHeartbeat)
	defer heartbeat.Stop()
	poll := time.NewTicker(eventsPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.Stopping:
			return
		case <-heartbeat.C:
			if check(ctx) != nil || write(": heartbeat\n\n") != nil {
				return
			}
			continue
		case <-wake:
		case <-poll.C:
		}
		topics, err := visibleTopics(ctx, d, store)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("app event stream failed", logx.Err, err)
			}
			return
		}
		seen := make(map[string]bool, len(topics))
		var out string
		for _, t := range topics {
			seen[t.Topic] = true
			if v, ok := sent[t.Topic]; ok && v == t.Revision {
				continue
			}
			sent[t.Topic] = t.Revision
			out += frame("change", t)
		}
		for topic := range sent {
			if !seen[topic] {
				delete(sent, topic)
			}
		}
		if out != "" && write(out) != nil {
			return
		}
	}
}
