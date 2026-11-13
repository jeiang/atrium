//go:build linux || darwin

package mediaagent

import (
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"golang.org/x/sys/unix"
)

func refusal(code string) error {
	return &protocol.Problem{Code: code, Mutation: protocol.MutationNone}
}
func (a *Agent) groupStat(st unix.Stat_t, dir bool) error {
	if a.gid < 0 {
		return nil
	}
	if int(st.Gid) != a.gid || st.Mode&0o040 == 0 {
		return refusal(protocol.CodePermissions)
	}
	if dir && st.Mode&0o030 != 0o030 {
		return refusal(protocol.CodePermissions)
	}
	return nil
}
func (a *Agent) parentChecks(p protocol.Path, missing map[protocol.Path]bool) ([]protocol.Step, error) {
	r := a.roots.byName[p.Root]
	if r == nil || !library(p.Root) {
		return nil, ErrUnsafePath
	}
	var st unix.Stat_t
	if err := unix.Fstat(r.fd, &st); err != nil {
		return nil, err
	}
	if err := a.groupStat(st, true); err != nil {
		return nil, err
	}
	var steps []protocol.Step
	parts := strings.Split(p.Rel, "/")
	for i := 1; i < len(parts); i++ {
		parent := protocol.Path{Root: p.Root, Rel: strings.Join(parts[:i], "/")}
		if missing[parent] {
			continue
		}
		st, err := a.roots.stat(parent)
		if errors.Is(err, ErrNotFound) {
			missing[parent] = true
			steps = append(steps, protocol.Step{Kind: protocol.StepMkdir, Destination: parent})
			continue
		}
		if err != nil {
			return nil, err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return nil, ErrNotRegular
		}
		if err := a.groupStat(st, true); err != nil {
			return nil, err
		}
	}
	return steps, nil
}
func (a *Agent) prepare(req protocol.PrepareRequest) (protocol.PrepareResponse, error) {
	if !a.roots.healthy() {
		return protocol.PrepareResponse{}, refusal(protocol.CodeRootChanged)
	}
	if len(req.Placements) > 1000 {
		return protocol.PrepareResponse{}, errListLimit
	}
	p := protocol.FilingPlan{Version: protocol.Version, AgentID: a.state.agentID, RootRevision: a.roots.revision, SourceKind: req.SourceKind, TorrentHash: req.TorrentHash}
	if req.SourceKind != protocol.SourceTorrent && req.SourceKind != protocol.SourceImport && req.SourceKind != protocol.SourceImportCleanup {
		return protocol.PrepareResponse{}, ErrUnsafePath
	}
	if req.Source.Root != sourceRoot(req.SourceKind) || validRel(req.Source.Rel) != nil {
		return protocol.PrepareResponse{}, ErrUnsafePath
	}
	if req.SourceKind == protocol.SourceTorrent && !validHash(req.TorrentHash) {
		return protocol.PrepareResponse{}, refusal(protocol.CodeInvalidRequest)
	}
	if req.Cleanup {
		if req.SourceKind != protocol.SourceImportCleanup || len(req.Placements) != 0 {
			return protocol.PrepareResponse{}, ErrUnsafePath
		}
		files, dirs, err := a.collect(req.Source)
		if err != nil {
			return protocol.PrepareResponse{}, err
		}
		for _, f := range files {
			if videoTypes[strings.ToLower(path.Ext(f.Path.Rel))] != "" {
				return protocol.PrepareResponse{}, refusal(protocol.CodeDestinationExists)
			}
			if err := a.checkSourceGroup(f.Path); err != nil {
				return protocol.PrepareResponse{}, err
			}
			id := f.Identity
			p.Steps = append(p.Steps, protocol.Step{Kind: protocol.StepUnlink, Destination: f.Path, Expect: &id})
		}
		slices.Reverse(dirs)
		for _, d := range dirs {
			id := d.Identity
			p.Steps = append(p.Steps, protocol.Step{Kind: protocol.StepRmdir, Destination: d.Path, Expect: &id})
		}
	} else {
		if req.SourceKind == protocol.SourceImportCleanup || len(req.Placements) == 0 || len(req.Placements) > 1000 {
			return protocol.PrepareResponse{}, ErrUnsafePath
		}
		missing := map[protocol.Path]bool{}
		destinations := map[protocol.Path]bool{}
		sources := map[protocol.Path]bool{}
		for _, placement := range req.Placements {
			src, dst := placement.Source, placement.Destination
			if src.Root != req.Source.Root || src.Rel != req.Source.Rel && !strings.HasPrefix(src.Rel, req.Source.Rel+"/") || validRel(dst.Rel) != nil || !library(dst.Root) || destinations[dst] || missing[dst] {
				return protocol.PrepareResponse{}, ErrUnsafePath
			}
			destinations[dst] = true
			if req.SourceKind == protocol.SourceImport && sources[src] {
				return protocol.PrepareResponse{}, ErrUnsafePath
			}
			sources[src] = true
			st, err := a.roots.stat(src)
			if err != nil {
				return protocol.PrepareResponse{}, err
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG {
				return protocol.PrepareResponse{}, ErrNotRegular
			}
			if err := a.groupStat(st, false); err != nil {
				return protocol.PrepareResponse{}, err
			}
			if uint64(st.Dev) != a.roots.byName[dst.Root].dev {
				return protocol.PrepareResponse{}, refusal(protocol.CodeCrossDevice)
			}
			parents, err := a.parentChecks(dst, missing)
			if err != nil {
				return protocol.PrepareResponse{}, err
			}
			for _, parent := range parents {
				if destinations[parent.Destination] {
					return protocol.PrepareResponse{}, ErrUnsafePath
				}
			}
			p.Steps = append(p.Steps, parents...)
			if _, err := a.roots.stat(dst); err == nil {
				return protocol.PrepareResponse{}, refusal(protocol.CodeDestinationExists)
			} else if !errors.Is(err, ErrNotFound) {
				return protocol.PrepareResponse{}, err
			}
			kind := protocol.StepRename
			if req.SourceKind == protocol.SourceTorrent {
				kind = protocol.StepLink
			}
			p.Steps = append(p.Steps, protocol.Step{Kind: kind, Source: &protocol.File{Path: src, Identity: identity(st)}, Destination: dst})
		}
	}
	if len(p.Steps) > 1000 {
		return protocol.PrepareResponse{}, errListLimit
	}
	for i := range p.Steps {
		p.Steps[i].Index = i
	}
	return protocol.PrepareResponse{Plan: p, PlanDigest: protocol.CanonicalDigest(p)}, nil
}
func (a *Agent) checkSourceGroup(p protocol.Path) error {
	st, err := a.roots.stat(p)
	if err != nil {
		return err
	}
	return a.groupStat(st, st.Mode&unix.S_IFMT == unix.S_IFDIR)
}
func validHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func (a *Agent) servePrepare(w http.ResponseWriter, r *http.Request) {
	var req protocol.PrepareRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	p, err := a.prepare(req)
	if err != nil {
		var problem *protocol.Problem
		if errors.As(err, &problem) {
			status := 409
			if problem.Code == protocol.CodeCrossDevice || problem.Code == protocol.CodePermissions {
				status = 422
			}
			if problem.Code == protocol.CodeInvalidRequest {
				status = 400
			}
			writeJSON(w, status, problem)
			return
		}
		listProblem(w, err)
		return
	}
	writeJSON(w, 200, p)
}
