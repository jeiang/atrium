package progress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
)

const (
	Heartbeat         = 15 * time.Second
	Idle              = time.Second
	WriteDeadline     = 10 * time.Second
	StreamsPerSubject = 4
	StreamsPerServer  = 16
)

var (
	ErrExpired = errors.New("progress: stream expired")
	ErrFuture  = errors.New("progress: cursor is beyond the stream")
	ErrBusy    = errors.New("progress: too many streams")
)

type Stream struct {
	RunID    int64
	After    int64
	Finished bool
}

func Open(ctx context.Context, q db.Querier, runID, after int64) (Stream, error) {
	b, err := Read(ctx, q, runID, after, 1)
	if err != nil {
		return Stream{}, err
	}
	switch {
	case after < 0 || after > b.Next:
		return Stream{}, ErrFuture
	case after < b.RetainedAfter:
		return Stream{}, ErrExpired
	}
	return Stream{RunID: runID, After: after, Finished: b.TerminalSeq != nil && after >= *b.TerminalSeq}, nil
}

type limits struct {
	sync.Mutex
	total int
	keys  map[string]int
}

var streamLimits = limits{keys: map[string]int{}}

func Acquire(key string) (func(), error) {
	streamLimits.Lock()
	defer streamLimits.Unlock()
	if streamLimits.total >= StreamsPerServer || streamLimits.keys[key] >= StreamsPerSubject {
		return nil, ErrBusy
	}
	streamLimits.total++
	streamLimits.keys[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			streamLimits.Lock()
			defer streamLimits.Unlock()
			streamLimits.total--
			if streamLimits.keys[key]--; streamLimits.keys[key] == 0 {
				delete(streamLimits.keys, key)
			}
		})
	}, nil
}

func Frame(e Event) (string, error) {
	decoded, err := jsonx.Loads(e.Data)
	if err != nil {
		return "", err
	}
	data := map[string]any{"run_id": e.RunID}
	fields, _ := decoded.(map[string]any)
	for k, v := range fields {
		data[k] = v
	}
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Kind, jsonx.Dumps(data, jsonx.Opts{NoEnsureASCII: true})), nil
}

type Live struct {
	Stopping <-chan struct{}
	Check    func(context.Context) error
	State    func(context.Context) (string, bool, error)
}

func Serve(ctx context.Context, w http.ResponseWriter, q db.Querier, s Stream, live Live) error {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "private, no-store, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	write := func(text string) error {
		_ = rc.SetWriteDeadline(time.Now().Add(WriteDeadline))
		if _, err := io.WriteString(w, text); err != nil {
			return err
		}
		return rc.Flush()
	}
	done := func(state string) {
		_ = write("event: done\ndata: " + jsonx.Dumps(map[string]any{"state": state}, jsonx.Opts{NoEnsureASCII: true}) + "\n\n")
	}
	wake, unsubscribe := Subscribe()
	defer unsubscribe()
	if err := write("retry: 1000\n\n"); err != nil {
		return nil
	}
	last := s.After
	ticker := time.NewTicker(Heartbeat)
	defer ticker.Stop()
	idle := time.NewTicker(Idle)
	defer idle.Stop()
	for {
		paused, seen, running := "", int64(0), ""
		for {
			b, err := Read(ctx, q, s.RunID, last, 100)
			if err != nil {
				return err
			}
			if last < b.RetainedAfter {
				return nil
			}
			seen = b.Next
			for _, e := range b.Events {
				frame, err := Frame(e)
				if err != nil {
					return err
				}
				if err := write(frame); err != nil {
					return nil
				}
				last = e.ID
				if b.TerminalSeq != nil && last >= *b.TerminalSeq {
					state, _, err := live.State(ctx)
					if err != nil {
						return err
					}
					done(state)
					return nil
				}
			}
			if len(b.Events) == 100 {
				continue
			}
			if paused != "" {
				done(paused)
				return nil
			}
			state, active, err := live.State(ctx)
			if err != nil {
				return err
			}
			if active {
				running = state
				break
			}
			paused = state
		}
	wait:
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-live.Stopping:
				return nil
			case <-wake:
				break wait
			case <-ticker.C:
				if live.Check(ctx) != nil {
					return nil
				}
				if err := write(": heartbeat\n\n"); err != nil {
					return nil
				}
				break wait
			case <-idle.C:
				moved, err := Moved(ctx, q, s.RunID, seen, running)
				if err != nil {
					return err
				}
				if moved {
					break wait
				}
			}
		}
	}
}
