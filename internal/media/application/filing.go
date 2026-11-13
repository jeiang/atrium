package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/media/agentclient"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const (
	FilingPreviewName application.Name = "media.filing-preview"
	FileItemName      application.Name = "media.file-item"
	IgnoreName        application.Name = "media.ignore"
	FilingInspectName application.Name = "media.filing-inspect"
)

type FilingInput struct {
	ID   string
	Form map[string]string
}

type FilingPreview struct {
	Item  media.Item
	View  media.View
	Ops   []media.Op
	Error string
}

type located struct {
	set   media.Settings
	agent *agentclient.Client
	qb    *media.Qbit
	item  media.Item
	shows []string
}

func DescribeFilingPreview() application.Definition {
	return application.Describe[FilingInput, FilingPreview](FilingPreviewName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) FilingPreview(ctx context.Context, in FilingInput) (application.Read[FilingPreview], error) {
	at, err := s.find(ctx, in.ID)
	if err != nil {
		return application.Read[FilingPreview]{}, err
	}
	out := at.preview(in.Form)
	if out.Error == "" {
		if _, err := at.prepare(ctx, out.Ops); err != nil {
			out.Error = prepareError(err).Error()
		}
	}
	rev, err := s.revision(ctx, string(FilingPreviewName), in, out)
	if err != nil {
		return application.Read[FilingPreview]{}, err
	}
	return application.Read[FilingPreview]{Revision: rev, Data: out}, nil
}

func (at located) preview(form map[string]string) FilingPreview {
	plan := at.set.Plan(at.set.Edited(at.item, at.shows, form))
	return FilingPreview{Item: at.item, View: plan.View, Ops: plan.Ops, Error: plan.Error}
}

func (at located) prepare(ctx context.Context, ops []media.Op) (protocol.PrepareResponse, error) {
	req := protocol.PrepareRequest{SourceKind: protocol.SourceImport, Source: at.item.Source, Placements: media.Placements(at.item, ops)}
	if at.item.Torrent {
		req.SourceKind, req.TorrentHash = protocol.SourceTorrent, strings.ToLower(at.item.ID)
	}
	return at.agent.Prepare(ctx, req)
}

func prepareError(err error) *application.Error {
	var e *agentclient.Error
	if errors.As(err, &e) && e.Mutation == protocol.MutationNone && e.Status < 500 {
		code := application.Invalid
		if e.Status == 409 {
			code = application.Conflict
		}
		return &application.Error{Code: code, Message: "the media agent refused the plan (" + e.Code + ")", Cause: err}
	}
	return &application.Error{Code: application.Unavailable, Message: "the media agent is unavailable", Cause: err}
}

type Filed struct {
	Item  media.Item
	Dests []string
}

func DescribeFileItem() application.Definition {
	return application.Describe[FilingInput, Filed](FileItemName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func (s *Service) FileItem(ctx context.Context, in FilingInput) (application.Change[Filed], error) {
	out, err := s.fileItem(ctx, in)
	id, err := s.record(ctx, FileItemName, in.ID, map[string]any{"placed": len(out.Dests)}, err)
	if err != nil {
		return application.Change[Filed]{}, err
	}
	return application.Change[Filed]{Data: out, AuditID: id}, nil
}

func (s *Service) fileItem(ctx context.Context, in FilingInput) (Filed, error) {
	at, err := s.find(ctx, in.ID)
	if err != nil {
		return Filed{}, err
	}
	plan := at.preview(in.Form)
	if plan.Error != "" {
		return Filed{}, invalid("plan", plan.Error)
	}
	if at.item.Torrent {
		current, err := at.qb.Torrent(ctx, at.item.ID)
		if err != nil {
			return Filed{}, classify(err)
		}
		if current == nil || !current.HasTag(media.TagMedia) || current.HasTag(media.TagFiled) || current.Progress < 1 {
			return Filed{}, invalid("id", "the torrent is no longer waiting")
		}
		if rel, ok := media.DownloadRel(at.set.DownloadPrefix, current.ContentPath); !ok || rel != at.item.Source.Rel {
			return Filed{}, invalid("id", "the torrent moved since it was listed")
		}
	}
	prep, err := at.prepare(ctx, plan.Ops)
	if err != nil {
		return Filed{}, prepareError(err)
	}
	state, result, err := s.runAgentFiling(ctx, at, media.OpFile, "media:file:", prep, media.AgentUndo)
	if err != nil {
		return Filed{}, err
	}
	dests := placed(prep.Plan, result)
	switch state {
	case ledger.Done:
	case ledger.Partial:
		return Filed{Item: at.item, Dests: dests}, classify(&media.PartialError{Cause: errors.New("the media agent completed only part of the filing"), Placed: dests})
	case ledger.Failed:
		return Filed{}, &application.Error{Code: application.Refused, Message: "the media agent could not file this item; nothing was placed"}
	default:
		return Filed{}, &application.Error{Code: application.Unknown, Message: "the media agent did not confirm the filing; inspect it and resolve the effect"}
	}
	if at.item.Torrent {
		if err := at.set.Tag(ctx, s.d.Ledger, at.set, media.Scope{}, at.item.ID, media.TagIgnored, false); err != nil {
			return Filed{Item: at.item, Dests: dests}, classifyFiling(&media.PartialError{Cause: err, Placed: dests})
		}
		if err := at.set.Tag(ctx, s.d.Ledger, at.set, media.Scope{}, at.item.ID, media.TagFiled, true); err != nil {
			return Filed{Item: at.item, Dests: dests}, classifyFiling(&media.PartialError{Cause: err, Placed: dests})
		}
	} else {
		s.cleanupImport(ctx, at)
	}
	return Filed{Item: at.item, Dests: dests}, nil
}

func placed(plan protocol.FilingPlan, result protocol.FilingResult) []string {
	done := map[int]bool{}
	for _, r := range result.Steps {
		if r.State == "done" {
			done[r.Index] = true
		}
	}
	out := []string{}
	for _, step := range plan.Steps {
		if (step.Kind == protocol.StepLink || step.Kind == protocol.StepRename) && done[step.Index] {
			out = append(out, libraryPath(step.Destination))
		}
	}
	return out
}

func libraryPath(p protocol.Path) string { return p.Root + "/" + p.Rel }

func agentResources(agentID string, plan protocol.FilingPlan) []ledger.Resource {
	seen := map[string]bool{}
	var out []ledger.Resource
	add := func(p protocol.Path) {
		key := "media-agent:" + agentID + ":" + p.Root + "/" + p.Rel
		if !seen[key] {
			seen[key] = true
			out = append(out, ledger.Resource{Key: key})
		}
	}
	for _, step := range plan.Steps {
		if step.Source != nil {
			add(step.Source.Path)
		}
		add(step.Destination)
	}
	return out
}

func (s *Service) runAgentFiling(ctx context.Context, at located, operation, prefix string, prep protocol.PrepareResponse, undo func(string, string) json.RawMessage) (ledger.State, protocol.FilingResult, error) {
	opKey := prefix + at.item.ID + ":" + ledger.NewKey()
	sum := sha256.Sum256([]byte("atrium-media-v1\n" + opKey + "\n" + prep.PlanDigest))
	key := hex.EncodeToString(sum[:])
	agentID := prep.Plan.AgentID
	n := ledger.New{
		Origin: "media", Operation: operation, OperationKey: opKey, Target: at.item.ID,
		Action:    mustJSON(media.AgentAction{Item: at.item.ID, Key: key, AgentID: agentID, PlanDigest: prep.PlanDigest, Plan: prep.Plan}),
		Resources: agentResources(agentID, prep.Plan),
	}
	if undo != nil {
		n.Undo = undo(agentID, key)
	}
	ticket, err := s.d.Ledger.Begin(ctx, n, nil)
	if err != nil {
		return "", protocol.FilingResult{}, classifyWrite(err)
	}
	result, ferr := s.execute(ctx, at.agent, ticket, protocol.FilingRequest{Key: key, PlanDigest: prep.PlanDigest, Plan: prep.Plan})
	s.d.Catalogue.Nudge()
	state, text := media.FilingState(result, ferr)
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	res := ledger.Result{State: state, Target: at.item.ID, Error: text}
	if ferr == nil {
		res.After = mustJSON(result)
	}
	if err := s.d.Ledger.Settle(settle, ticket, res, nil); err != nil {
		return "", result, err
	}
	return state, result, nil
}

func (s *Service) execute(ctx context.Context, agent *agentclient.Client, ticket ledger.Ticket, req protocol.FilingRequest) (protocol.FilingResult, error) {
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	defer cancel()
	type answer struct {
		result protocol.FilingResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		r, err := agent.File(work, req)
		done <- answer{r, err}
	}()
	after := 0
	for {
		r, err := agent.Filing(work, req.Key, after, 20*time.Second)
		if err == nil && len(r.Steps) > after {
			after = len(r.Steps)
			_ = s.d.Ledger.Progress(work, ticket, mustJSON(r))
		}
		pause := time.After(0)
		if err != nil || r.State != "running" {
			pause = time.After(250 * time.Millisecond)
		}
		select {
		case a := <-done:
			return a.result, a.err
		case <-pause:
		}
	}
}

func (s *Service) cleanupImport(ctx context.Context, at located) {
	src, err := at.agent.Source(ctx, at.item.Source)
	if err != nil {
		return
	}
	for _, f := range src.Files {
		if at.set.VideoExts[strings.ToLower(media.Ext(path.Base(f.Path.Rel)))] {
			return
		}
	}
	prep, err := at.agent.Prepare(ctx, protocol.PrepareRequest{SourceKind: protocol.SourceImportCleanup, Source: at.item.Source, Cleanup: true})
	if err != nil {
		return
	}
	_, _, _ = s.runAgentFiling(ctx, at, media.OpImportCleanup, "media:import-cleanup:", prep, nil)
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

type IgnoreInput struct {
	ID string
}

type Ignored struct {
	ID string
}

func DescribeIgnore() application.Definition {
	return application.Describe[IgnoreInput, Ignored](IgnoreName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func (s *Service) Ignore(ctx context.Context, in IgnoreInput) (application.Change[Ignored], error) {
	err := s.ignore(ctx, in)
	id, err := s.record(ctx, IgnoreName, in.ID, nil, err)
	if err != nil {
		return application.Change[Ignored]{}, err
	}
	return application.Change[Ignored]{Data: Ignored(in), AuditID: id}, nil
}

func (s *Service) ignore(ctx context.Context, in IgnoreInput) error {
	if in.ID == "" {
		return invalid("id", "Field required")
	}
	at, err := s.find(ctx, in.ID)
	if err != nil {
		return err
	}
	return classifyFiling(media.Ignore(ctx, s.d.Ledger, at.set, at.item))
}

func (s *Service) find(ctx context.Context, id string) (located, error) {
	var at located
	set, err := s.settings(ctx)
	if err != nil {
		return at, err
	}
	at.set = set
	if at.qb, err = set.Qbit(); err != nil {
		return at, classify(err)
	}
	if at.agent, err = set.Agent(); err != nil {
		return at, classify(err)
	}
	if at.item, err = set.Find(ctx, at.qb, at.agent, set.DownloadPrefix, id); err != nil {
		return at, classify(err)
	}
	at.shows = media.Shows(ctx, at.agent)
	return at, nil
}

func classifyFiling(err error) error {
	var partial *media.PartialError
	if errors.As(err, &partial) {
		return classify(err)
	}
	var plan media.PlanError
	if errors.As(err, &plan) {
		return &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
	}
	return classifyWrite(err)
}

type InspectFilingInput struct {
	EffectID int64
}

type FilingInspection struct {
	EffectID int64               `json:"effect_id"`
	State    string              `json:"state"`
	Key      string              `json:"key"`
	AgentID  string              `json:"agent_id"`
	Receipt  protocol.Inspection `json:"receipt"`
}

func DescribeFilingInspect() application.Definition {
	return application.Describe[InspectFilingInput, FilingInspection](FilingInspectName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) InspectFiling(ctx context.Context, in InspectFilingInput) (application.Read[FilingInspection], error) {
	e, err := s.d.Ledger.Get(ctx, in.EffectID)
	var missing *ledger.NotFound
	if errors.As(err, &missing) {
		return application.Read[FilingInspection]{}, notFound("unknown effect")
	}
	if err != nil {
		return application.Read[FilingInspection]{}, err
	}
	var action media.AgentAction
	if e.Operation != media.OpFile && e.Operation != media.OpImportCleanup {
		return application.Read[FilingInspection]{}, notFound("not a media filing")
	}
	if json.Unmarshal(e.Action, &action) != nil || action.Key == "" {
		return application.Read[FilingInspection]{}, invalid("effect", "filed before the media agent; there is no receipt to inspect")
	}
	set, err := s.settings(ctx)
	if err != nil {
		return application.Read[FilingInspection]{}, err
	}
	agent, err := set.Agent()
	if err != nil {
		return application.Read[FilingInspection]{}, classify(err)
	}
	receipt, err := agent.Inspect(ctx, action.Key)
	if err != nil {
		return application.Read[FilingInspection]{}, prepareError(err)
	}
	out := FilingInspection{EffectID: e.ID, State: string(e.State), Key: action.Key, AgentID: action.AgentID, Receipt: receipt}
	rev, err := s.revision(ctx, string(FilingInspectName), in, out)
	if err != nil {
		return application.Read[FilingInspection]{}, err
	}
	return application.Read[FilingInspection]{Revision: rev, Data: out}, nil
}
