//go:build linux || darwin

package mediaagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

type receipt struct {
	started bool
	mu      sync.Mutex
	request protocol.FilingRequest
	result  protocol.FilingResult
	changed chan struct{}
	dir     string
}
type preparedStep struct {
	Step         protocol.Step      `json:"step"`
	Parent       protocol.Identity  `json:"parent"`
	SourceParent *protocol.Identity `json:"source_parent,omitempty"`
}

func durableJSON(dir, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeDurable(dir, name, b)
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}
func (r *receipt) publish(result protocol.FilingResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = result
	close(r.changed)
	r.changed = make(chan struct{})
}
func (r *receipt) snapshot() (protocol.FilingResult, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := r.result
	result.Steps = append([]protocol.StepResult{}, result.Steps...)
	return result, r.changed
}
func keyValid(key string) bool {
	if len(key) != 64 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func (a *Agent) loadReceipts() error {
	base := filepath.Join(a.state.dir, "filings")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	if err := syncDir(a.state.dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	a.receipts = map[string]*receipt{}
	for _, entry := range entries {
		if !entry.IsDir() || !keyValid(entry.Name()) {
			return errors.New("the receipt directory is invalid")
		}
		key := entry.Name()
		dir := filepath.Join(base, key)
		r := &receipt{started: true, dir: dir, changed: make(chan struct{})}
		err := readJSON(filepath.Join(dir, "plan.json"), &r.request)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (r.request.Key != key || protocol.CanonicalDigest(r.request.Plan) != r.request.PlanDigest) {
			return errors.New("the receipt plan is invalid")
		}
		err = readJSON(filepath.Join(dir, "result.json"), &r.result)
		if errors.Is(err, os.ErrNotExist) {
			if err := a.recoverReceipt(key, r); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		a.receipts[key] = r
	}
	return nil
}
func (a *Agent) recoverReceipt(key string, r *receipt) error {
	result := protocol.FilingResult{Key: key, PlanDigest: r.request.PlanDigest, State: "unknown", Steps: []protocol.StepResult{}, ErrorCode: protocol.CodeUnavailable}
	if r.request.Plan.AgentID != a.state.agentID || r.request.Plan.RootRevision != a.roots.revision {
		r.result = result
		return durableJSON(r.dir, "result.json", result)
	}
	for i, s := range r.request.Plan.Steps {
		var done protocol.StepResult
		err := readJSON(filepath.Join(r.dir, fmt.Sprintf("step-%d.done", i)), &done)
		if err == nil {
			result.Steps = append(result.Steps, done)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var prepared preparedStep
		err = readJSON(filepath.Join(r.dir, fmt.Sprintf("step-%d.prepared", i)), &prepared)
		if errors.Is(err, os.ErrNotExist) {
			if len(result.Steps) > 0 {
				result.State = "partial"
			}
			break
		}
		if err != nil {
			return err
		}
		if prepared.Step.Index != i {
			return errors.New("the prepared receipt is invalid")
		}
		confirmed, after := a.confirmPrepared(prepared)
		if confirmed {
			done = protocol.StepResult{Index: i, State: "done", After: after}
			if s.Source != nil {
				before := s.Source.Identity
				done.Before = &before
			}
			if err := durableJSON(r.dir, fmt.Sprintf("step-%d.done", i), done); err != nil {
				return err
			}
			result.Steps = append(result.Steps, done)
		}
		break
	}
	if len(result.Steps) == len(r.request.Plan.Steps) && len(result.Steps) > 0 {
		result.State = "done"
		result.ErrorCode = ""
	}
	r.result = result
	return durableJSON(r.dir, "result.json", result)
}
func (a *Agent) confirmPrepared(p preparedStep) (bool, *protocol.Identity) {
	s := p.Step
	fd, _, err := a.roots.parent(s.Destination)
	if err != nil {
		return false, nil
	}
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	_ = unix.Close(fd)
	if err != nil || !sameObject(identity(st), p.Parent) {
		return false, nil
	}
	dst, err := a.roots.stat(s.Destination)
	if err != nil {
		return false, nil
	}
	id := identity(dst)
	if s.Source == nil || id != s.Source.Identity || dst.Mode&unix.S_IFMT != unix.S_IFREG {
		return false, nil
	}
	if p.SourceParent == nil {
		return false, nil
	}
	sp, _, err := a.roots.parent(s.Source.Path)
	if err != nil {
		return false, nil
	}
	var parent unix.Stat_t
	err = unix.Fstat(sp, &parent)
	_ = unix.Close(sp)
	if err != nil || !sameObject(identity(parent), *p.SourceParent) {
		return false, nil
	}
	src, err := a.roots.stat(s.Source.Path)
	switch s.Kind {
	case protocol.StepLink:
		if err == nil && identity(src) == s.Source.Identity {
			return true, &id
		}
	case protocol.StepRename:
		if errors.Is(err, ErrNotFound) {
			return true, &id
		}
	}
	return false, nil
}
