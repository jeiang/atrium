package runtime

import (
	"errors"
	"maps"
	"slices"
	"sync"
	"time"
)

type Status string

const (
	StatusStopped  Status = "stopped"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusBackoff  Status = "backoff"
)

type ServiceHealth struct {
	Name         string
	Critical     bool
	Status       Status
	Since        time.Time
	Restarts     int
	LastError    string
	Stack        string
	NextAttempt  time.Time
	Heartbeats   map[string]time.Time
	Stalled      bool
	StalledLoops []string
}

type Health struct {
	Started  bool
	Draining bool
	Ready    bool
	Degraded bool
	Services []ServiceHealth
}

type state struct {
	mu       sync.Mutex
	name     string
	critical bool
	stall    time.Duration
	status   Status
	since    time.Time
	restarts int
	lastErr  string
	stack    string
	next     time.Time
	beats    map[string]time.Time
}

func (st *state) set(status Status) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.status, st.since, st.next = status, time.Now(), time.Time{}
}

func (st *state) begin() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.status, st.since, st.next, st.beats = StatusRunning, time.Now(), time.Time{}, nil
}

func (st *state) fail(err error, next time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.status, st.since, st.next = StatusBackoff, time.Now(), next
	st.restarts++
	st.lastErr, st.stack = err.Error(), ""
	var p *PanicError
	if errors.As(err, &p) {
		st.stack = p.Stack
	}
}

func (st *state) heartbeat(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.beats == nil {
		st.beats = map[string]time.Time{}
	}
	st.beats[name] = time.Now()
}

func (st *state) snapshot(now time.Time) ServiceHealth {
	st.mu.Lock()
	defer st.mu.Unlock()
	h := ServiceHealth{Name: st.name, Critical: st.critical, Status: st.status, Since: st.since, Restarts: st.restarts,
		LastError: st.lastErr, Stack: st.stack, NextAttempt: st.next, Heartbeats: maps.Clone(st.beats)}
	if st.stall > 0 && st.status == StatusRunning {
		if len(st.beats) == 0 {
			h.Stalled = now.Sub(st.since) > st.stall
		}
		for name, at := range st.beats {
			if now.Sub(at) > st.stall {
				h.StalledLoops = append(h.StalledLoops, name)
			}
		}
		slices.Sort(h.StalledLoops)
		h.Stalled = h.Stalled || len(h.StalledLoops) > 0
	}
	return h
}

func (s *Supervisor) Health() Health {
	s.mu.Lock()
	h := Health{Started: s.started, Draining: s.draining}
	s.mu.Unlock()
	now := time.Now()
	h.Ready = h.Started && !h.Draining
	for _, def := range s.defs {
		sh := s.states[def.Name].snapshot(now)
		healthy := sh.Status == StatusRunning && !sh.Stalled
		if !healthy && sh.Critical {
			h.Ready = false
		}
		if !healthy && !sh.Critical && h.Started && !h.Draining {
			h.Degraded = true
		}
		h.Services = append(h.Services, sh)
	}
	return h
}
