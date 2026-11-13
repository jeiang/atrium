//go:build linux || darwin

package mediaagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

type receipt struct {
	started     bool
	quarantined bool
	mu          sync.Mutex
	request     protocol.FilingRequest
	result      protocol.FilingResult
	changed     chan struct{}
	dir         string
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

var errUnadmitted = errors.New("the receipt was never admitted")

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
		r, err := a.loadReceipt(key, dir)
		if errors.Is(err, errUnadmitted) {
			if err := os.RemoveAll(dir); err == nil {
				if err := syncDir(base); err != nil {
					return err
				}
				continue
			}
		}
		if err != nil {
			r = &receipt{started: true, quarantined: true, dir: dir, changed: make(chan struct{}), result: protocol.FilingResult{Key: key, State: "unknown", Steps: []protocol.StepResult{}, ErrorCode: protocol.CodeReceiptInvalid}}
			a.quarantined = append(a.quarantined, key)
		}
		a.receipts[key] = r
	}
	return nil
}
func (a *Agent) Quarantined() []string { return a.quarantined }
func (a *Agent) loadReceipt(key, dir string) (*receipt, error) {
	r := &receipt{started: true, dir: dir, changed: make(chan struct{})}
	err := readJSON(filepath.Join(dir, "plan.json"), &r.request)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errUnadmitted
	}
	if err != nil {
		return nil, err
	}
	req := r.request
	if req.Key != key || protocol.CanonicalDigest(req.Plan) != req.PlanDigest || len(req.Plan.Steps) == 0 || req.UndoOf != "" && protocol.UndoKey(req.UndoOf) != key {
		return nil, errInvalidReceipt
	}
	for i, s := range req.Plan.Steps {
		if s.Index != i {
			return nil, errInvalidReceipt
		}
	}
	err = readJSON(filepath.Join(dir, "result.json"), &r.result)
	if errors.Is(err, os.ErrNotExist) {
		return r, a.recoverReceipt(r)
	}
	if err != nil {
		return nil, err
	}
	res := r.result
	switch {
	case res.Key != key || res.PlanDigest != req.PlanDigest || res.Steps == nil || len(res.Steps) > len(req.Plan.Steps):
		return nil, errInvalidReceipt
	case res.State == "done" && len(res.Steps) != len(req.Plan.Steps):
		return nil, errInvalidReceipt
	case res.State != "done" && res.State != "partial" && res.State != "failed" && res.State != "unknown":
		return nil, errInvalidReceipt
	}
	for i, s := range res.Steps {
		if s.Index != i || s.State != "done" {
			return nil, errInvalidReceipt
		}
	}
	return r, nil
}

var errInvalidReceipt = errors.New("the receipt does not match its plan")

type stepOutcome int

const (
	stepUndetermined stepOutcome = iota
	stepNotDone
	stepDone
)

func (a *Agent) recoverReceipt(r *receipt) error {
	plan := r.request.Plan
	result := protocol.FilingResult{Key: r.request.Key, PlanDigest: r.request.PlanDigest, State: "failed", Steps: []protocol.StepResult{}, ErrorCode: protocol.CodeUnavailable}
	sameRoots := plan.AgentID == a.state.agentID && plan.RootRevision == a.roots.revision
	for i, s := range plan.Steps {
		var done protocol.StepResult
		err := readJSON(filepath.Join(r.dir, fmt.Sprintf("step-%d.done", i)), &done)
		if err == nil {
			if done.Index != i || done.State != "done" {
				return errInvalidReceipt
			}
			result.Steps = append(result.Steps, done)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var prepared preparedStep
		err = readJSON(filepath.Join(r.dir, fmt.Sprintf("step-%d.prepared", i)), &prepared)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(prepared.Step, s) {
			return errInvalidReceipt
		}
		outcome, step := stepUndetermined, protocol.StepResult{}
		if sameRoots {
			outcome, step = a.inspectPrepared(prepared)
		}
		if outcome == stepNotDone {
			break
		}
		if outcome == stepUndetermined {
			result.State = "unknown"
			if !sameRoots {
				result.ErrorCode = protocol.CodeRootChanged
			}
			r.result = result
			return durableJSON(r.dir, "result.json", result)
		}
		if err := durableJSON(r.dir, fmt.Sprintf("step-%d.done", i), step); err != nil {
			return err
		}
		result.Steps = append(result.Steps, step)
	}
	switch {
	case len(result.Steps) == len(plan.Steps):
		result.State, result.ErrorCode = "done", ""
	case len(result.Steps) > 0:
		result.State = "partial"
	}
	r.result = result
	return durableJSON(r.dir, "result.json", result)
}
func (a *Agent) parentMatches(p protocol.Path, want *protocol.Identity) bool {
	if want == nil {
		return false
	}
	fd, _, err := a.roots.parent(p)
	if err != nil {
		return false
	}
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	_ = unix.Close(fd)
	return err == nil && sameObject(identity(st), *want)
}
func (a *Agent) inspectPrepared(p preparedStep) (stepOutcome, protocol.StepResult) {
	s := p.Step
	result := protocol.StepResult{Index: s.Index, State: "done"}
	if !a.parentMatches(s.Destination, &p.Parent) {
		return stepUndetermined, result
	}
	dst, dstErr := a.roots.stat(s.Destination)
	if dstErr != nil && !errors.Is(dstErr, ErrNotFound) {
		return stepUndetermined, result
	}
	dstID := identity(dst)
	switch s.Kind {
	case protocol.StepLink, protocol.StepRename:
		if s.Source == nil || !a.parentMatches(s.Source.Path, p.SourceParent) {
			return stepUndetermined, result
		}
		src, srcErr := a.roots.stat(s.Source.Path)
		if srcErr != nil && !errors.Is(srcErr, ErrNotFound) {
			return stepUndetermined, result
		}
		srcHere := srcErr == nil && identity(src) == s.Source.Identity && src.Mode&unix.S_IFMT == unix.S_IFREG
		dstHere := dstErr == nil && dstID == s.Source.Identity && dst.Mode&unix.S_IFMT == unix.S_IFREG
		before := s.Source.Identity
		result.Before, result.After = &before, &dstID
		switch {
		case errors.Is(dstErr, ErrNotFound) && srcHere:
			return stepNotDone, result
		case s.Kind == protocol.StepLink && dstHere && srcHere:
			return stepDone, result
		case s.Kind == protocol.StepRename && dstHere && errors.Is(srcErr, ErrNotFound):
			return stepDone, result
		}
	case protocol.StepMkdir:
		if errors.Is(dstErr, ErrNotFound) {
			return stepNotDone, result
		}
		mode := uint32(0o775)
		if a.gid >= 0 {
			mode = 0o2775
		}
		if dst.Mode&unix.S_IFMT == unix.S_IFDIR && uint32(dst.Mode)&0o7777 == mode && (a.gid < 0 || int(dst.Gid) == a.gid) {
			result.After = &dstID
			return stepDone, result
		}
	case protocol.StepUnlink, protocol.StepRmdir:
		if s.Expect == nil {
			return stepUndetermined, result
		}
		expect := *s.Expect
		result.Before = &expect
		if errors.Is(dstErr, ErrNotFound) {
			return stepDone, result
		}
		if s.Kind == protocol.StepUnlink && dstID == expect || s.Kind == protocol.StepRmdir && sameObject(dstID, expect) {
			return stepNotDone, result
		}
	}
	return stepUndetermined, result
}
