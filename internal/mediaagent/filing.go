//go:build linux || darwin

package mediaagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

func (a *Agent) validatePlan(p protocol.FilingPlan, undo bool) error {
	if !a.roots.healthy() {
		return refusal(protocol.CodeRootChanged)
	}
	if p.Version != protocol.Version || p.AgentID != a.state.agentID || p.RootRevision != a.roots.revision {
		return refusal(protocol.CodeRootChanged)
	}
	if !undo && p.SourceKind != protocol.SourceTorrent && p.SourceKind != protocol.SourceImport && p.SourceKind != protocol.SourceImportCleanup {
		return ErrUnsafePath
	}
	if len(p.Steps) > 1000 {
		return refusal(protocol.CodeRequestTooLarge)
	}
	if len(p.Steps) == 0 {
		return refusal(protocol.CodeInvalidRequest)
	}
	for i, s := range p.Steps {
		if s.Index != i || validRel(s.Destination.Rel) != nil || a.roots.byName[s.Destination.Root] == nil {
			return ErrUnsafePath
		}
		if undo {
			continue
		}
		if s.Source != nil && (validRel(s.Source.Path.Rel) != nil || s.Source.Path.Root != sourceRoot(p.SourceKind)) {
			return ErrUnsafePath
		}
		switch s.Kind {
		case protocol.StepMkdir:
			if !library(s.Destination.Root) || s.Source != nil || s.Expect != nil {
				return ErrUnsafePath
			}
		case protocol.StepLink:
			if p.SourceKind != protocol.SourceTorrent || !validHash(p.TorrentHash) || s.Source == nil || !library(s.Destination.Root) || s.Expect != nil {
				return ErrUnsafePath
			}
		case protocol.StepRename:
			if p.SourceKind != protocol.SourceImport || s.Source == nil || !library(s.Destination.Root) || s.Expect != nil {
				return ErrUnsafePath
			}
		case protocol.StepUnlink, protocol.StepRmdir:
			if p.SourceKind != protocol.SourceImportCleanup || s.Destination.Root != protocol.RootImport || s.Source != nil || s.Expect == nil {
				return ErrUnsafePath
			}
		default:
			return ErrUnsafePath
		}
		if p.SourceKind == protocol.SourceImportCleanup {
			if s.Kind == protocol.StepUnlink && keptVideo(s.Destination.Rel, s.Expect.Size) {
				return refusal(protocol.CodeDestinationExists)
			}
			item := protocol.Path{Root: protocol.RootImport, Rel: strings.Split(s.Destination.Rel, "/")[0]}
			files, _, err := a.collect(item)
			if err != nil {
				return err
			}
			for _, f := range files {
				if keptVideo(f.Path.Rel, f.Identity.Size) {
					return refusal(protocol.CodeDestinationExists)
				}
			}
		}
	}
	return nil
}
func (a *Agent) inverse(key string) (protocol.FilingPlan, error) {
	a.receiptMu.Lock()
	r := a.receipts[key]
	a.receiptMu.Unlock()
	if r == nil {
		return protocol.FilingPlan{}, refusal(protocol.CodeNotFound)
	}
	result, _ := r.snapshot()
	if result.State != "done" && result.State != "partial" {
		return protocol.FilingPlan{}, refusal(protocol.CodeBusy)
	}
	p := protocol.FilingPlan{Version: protocol.Version, AgentID: a.state.agentID, RootRevision: a.roots.revision, SourceKind: "undo", TorrentHash: key}
	created := map[protocol.Path]bool{}
	for i := len(result.Steps) - 1; i >= 0; i-- {
		res := result.Steps[i]
		if res.State != "done" || res.After == nil {
			continue
		}
		if res.Index < 0 || res.Index >= len(r.request.Plan.Steps) {
			return protocol.FilingPlan{}, refusal(protocol.CodeReceiptInvalid)
		}
		s := r.request.Plan.Steps[res.Index]
		id := *res.After
		switch s.Kind {
		case protocol.StepLink:
			s = protocol.Step{Kind: protocol.StepUnlink, Destination: s.Destination, Expect: &id, Source: s.Source}
		case protocol.StepRename:
			back := s.Source.Path
			st, err := a.roots.stat(s.Destination)
			if err != nil {
				return protocol.FilingPlan{}, err
			}
			if identity(st) != id || st.Mode&unix.S_IFMT != unix.S_IFREG {
				return protocol.FilingPlan{}, refusal(protocol.CodeIdentityChanged)
			}
			parts := strings.Split(back.Rel, "/")
			for j := 1; j < len(parts); j++ {
				dir := protocol.Path{Root: back.Root, Rel: strings.Join(parts[:j], "/")}
				if created[dir] {
					continue
				}
				if _, err := a.roots.stat(dir); !errors.Is(err, ErrNotFound) {
					continue
				}
				created[dir] = true
				p.Steps = append(p.Steps, protocol.Step{Index: len(p.Steps), Kind: protocol.StepMkdir, Destination: dir})
			}
			s = protocol.Step{Kind: protocol.StepRename, Source: &protocol.File{Path: s.Destination, Identity: id}, Destination: back}
		case protocol.StepMkdir:
			s = protocol.Step{Kind: protocol.StepRmdir, Destination: s.Destination, Expect: &id}
		default:
			return protocol.FilingPlan{}, refusal(protocol.CodeUnsupportedFile)
		}
		s.Index = len(p.Steps)
		p.Steps = append(p.Steps, s)
	}
	return p, nil
}
func (a *Agent) execute(r *receipt) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	result, _ := r.snapshot()
	finish := func(state, code string) {
		result.State = state
		result.ErrorCode = code
		if err := durableJSON(r.dir, "result.json", result); err != nil {
			result.State = "unknown"
			result.ErrorCode = protocol.CodeUnavailable
		}
		r.publish(result)
	}
	for _, step := range r.request.Plan.Steps {
		if !a.roots.healthy() {
			state := "failed"
			if len(result.Steps) > 0 {
				state = "partial"
			}
			finish(state, protocol.CodeRootChanged)
			return
		}
		if a.draining.Load() {
			state := "failed"
			if len(result.Steps) > 0 {
				state = "partial"
			}
			finish(state, protocol.CodeDraining)
			return
		}
		res, mutated, err := a.executeStep(r.dir, step, r.request.UndoOf != "")
		if err != nil {
			code := codeOf(err)
			var p *protocol.Problem
			if errors.As(err, &p) {
				code = p.Code
			}
			state := "failed"
			if mutated {
				state = "unknown"
			} else if len(result.Steps) > 0 {
				state = "partial"
			}
			finish(state, code)
			return
		}
		result.Steps = append(result.Steps, res)
		r.publish(result)
	}
	finish("done", "")
}
func (a *Agent) executeStep(dir string, s protocol.Step, undo bool) (protocol.StepResult, bool, error) {
	result := protocol.StepResult{Index: s.Index, State: "done"}
	dp, leaf, err := a.roots.parent(s.Destination)
	if err != nil {
		return result, false, err
	}
	defer func() { _ = unix.Close(dp) }()
	var dstParent unix.Stat_t
	if err := unix.Fstat(dp, &dstParent); err != nil {
		return result, false, err
	}
	if !undo || library(s.Destination.Root) {
		if err := a.groupStat(dstParent, true); err != nil {
			return result, false, err
		}
	}
	prepared := preparedStep{Step: s, Parent: identity(dstParent)}
	sp := -1
	srcLeaf := ""
	var before unix.Stat_t
	if s.Source != nil {
		sp, srcLeaf, err = a.roots.parent(s.Source.Path)
		if err != nil {
			return result, false, err
		}
		defer func() { _ = unix.Close(sp) }()
		if err = unix.Fstatat(sp, srcLeaf, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return result, false, walkError(err)
		}
		if before.Mode&unix.S_IFMT != unix.S_IFREG || identity(before) != s.Source.Identity {
			return result, false, refusal(protocol.CodeIdentityChanged)
		}
		if err := a.groupStat(before, false); err != nil {
			return result, false, err
		}
		if before.Dev != dstParent.Dev {
			return result, false, refusal(protocol.CodeCrossDevice)
		}
		var parent unix.Stat_t
		if err := unix.Fstat(sp, &parent); err != nil {
			return result, false, err
		}
		sourceParent, sourceID := identity(parent), s.Source.Identity
		prepared.SourceParent, result.Before = &sourceParent, &sourceID
	}
	var existing unix.Stat_t
	err = unix.Fstatat(dp, leaf, &existing, unix.AT_SYMLINK_NOFOLLOW)
	if s.Expect == nil {
		if err == nil {
			return result, false, refusal(protocol.CodeDestinationExists)
		}
		if !errors.Is(err, unix.ENOENT) {
			return result, false, walkError(err)
		}
	} else {
		if err != nil {
			return result, false, walkError(err)
		}
		id := identity(existing)
		if !sameObject(id, *s.Expect) || (s.Kind == protocol.StepUnlink && id != *s.Expect) {
			return result, false, refusal(protocol.CodeIdentityChanged)
		}
		if s.Kind == protocol.StepUnlink && existing.Mode&unix.S_IFMT != unix.S_IFREG || s.Kind == protocol.StepRmdir && existing.Mode&unix.S_IFMT != unix.S_IFDIR {
			return result, false, ErrNotRegular
		}
		result.Before = &id
	}
	if err := durableJSON(dir, fmt.Sprintf("step-%d.prepared", s.Index), prepared); err != nil {
		return result, false, err
	}
	switch s.Kind {
	case protocol.StepMkdir:
		err = unix.Mkdirat(dp, leaf, 0o775)
		if err == nil {
			fd, e := openat(dp, leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC)
			if e != nil {
				return result, true, e
			}
			if a.gid >= 0 {
				e = unix.Fchown(fd, -1, a.gid)
				if e == nil {
					e = unix.Fchmod(fd, 0o2775)
				}
			} else {
				e = unix.Fchmod(fd, 0o775)
			}
			if e == nil {
				e = unix.Fsync(fd)
			}
			_ = unix.Close(fd)
			if e != nil {
				if unix.Unlinkat(dp, leaf, unix.AT_REMOVEDIR) == nil && unix.Fsync(dp) == nil {
					return result, false, e
				}
				return result, true, e
			}
		}
	case protocol.StepLink:
		err = unix.Linkat(sp, srcLeaf, dp, leaf, 0)
	case protocol.StepRename:
		err = renameExclusive(sp, srcLeaf, dp, leaf)
	case protocol.StepUnlink:
		err = unix.Unlinkat(dp, leaf, 0)
	case protocol.StepRmdir:
		err = unix.Unlinkat(dp, leaf, unix.AT_REMOVEDIR)
		if undo && (errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST)) {
			err = nil
		}
	default:
		return result, false, ErrUnsafePath
	}
	if err != nil {
		return result, false, err
	}
	if err := unix.Fsync(dp); err != nil {
		return result, true, err
	}
	if sp >= 0 && sp != dp {
		if err := unix.Fsync(sp); err != nil {
			return result, true, err
		}
	}
	if s.Kind == protocol.StepLink || s.Kind == protocol.StepRename || s.Kind == protocol.StepMkdir {
		var after unix.Stat_t
		if err := unix.Fstatat(dp, leaf, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return result, true, err
		}
		id := identity(after)
		if s.Source != nil && (id != s.Source.Identity || after.Mode&unix.S_IFMT != unix.S_IFREG) {
			return result, true, refusal(protocol.CodeIdentityChanged)
		}
		if s.Kind == protocol.StepLink {
			var now unix.Stat_t
			if err := unix.Fstatat(sp, srcLeaf, &now, unix.AT_SYMLINK_NOFOLLOW); err != nil || identity(now) != s.Source.Identity {
				return result, true, refusal(protocol.CodeIdentityChanged)
			}
		}
		result.After = &id
	}
	if err := durableJSON(dir, fmt.Sprintf("step-%d.done", s.Index), result); err != nil {
		return result, true, err
	}
	return result, true, nil
}
func (a *Agent) admit(req protocol.FilingRequest) (*receipt, error) {
	a.receiptMu.Lock()
	defer a.receiptMu.Unlock()
	if r := a.receipts[req.Key]; r != nil {
		if r.quarantined {
			return r, nil
		}
		if r.request.PlanDigest != req.PlanDigest || r.request.UndoOf != req.UndoOf {
			return nil, refusal(protocol.CodeKeyReused)
		}
		return r, nil
	}
	if a.draining.Load() {
		return nil, refusal(protocol.CodeDraining)
	}
	base := filepath.Join(a.state.dir, "filings")
	dir := filepath.Join(base, req.Key)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	err := syncDir(base)
	if err == nil {
		err = durableJSON(dir, "plan.json", req)
	}
	if err != nil {
		if os.RemoveAll(dir) == nil {
			_ = syncDir(base)
		}
		return nil, err
	}
	r := &receipt{request: req, dir: dir, changed: make(chan struct{}), result: protocol.FilingResult{Key: req.Key, PlanDigest: req.PlanDigest, State: "running", Steps: []protocol.StepResult{}}}
	a.receipts[req.Key] = r
	return r, nil
}
