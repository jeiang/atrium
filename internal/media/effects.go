package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media/agentclient"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const (
	OpAddMagnet     = "media_add_magnet"
	OpAddFile       = "media_add_file"
	OpPriority      = "media_file_priority"
	OpTagAdd        = "media_tag_add"
	OpTagRemove     = "media_tag_remove"
	OpStart         = "media_start"
	OpDelete        = "media_delete"
	OpFile          = "media_file"
	OpImportCleanup = "media_import_cleanup"

	UndoTorrent     = "media_torrent_delete"
	UndoFilePlace   = "media_file_place"
	UndoAgentUnfile = "media_agent_unfile"
)

type torrentUndo struct {
	Op   string `json:"op"`
	Hash string `json:"hash"`
}

type Scope struct {
	ProposalID *int64
	ThreadID   *int64
	RunID      *int64
}

type AddRequest struct {
	Magnet       string
	Data         []byte
	Name         string
	Tags         string
	OperationKey string
	Scope        Scope
	Done         ledger.Mutation
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func validateOp(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("media undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("media undo payload names " + head.Op)
		}
		return nil
	}
}

func qbitKey(set Settings, hash string) (string, error) {
	base, err := set.NeedQbit()
	if err != nil {
		return "", err
	}
	return "qbit:" + strings.TrimRight(base, "/") + ":" + strings.ToLower(hash), nil
}

func settleState(err error) ledger.State {
	if QbitDefinite(err) {
		return ledger.Failed
	}
	return ledger.Unknown
}

func (t Tuning) Add(ctx context.Context, store *ledger.Store, set Settings, req AddRequest) (string, error) {
	hash := ""
	operation := OpAddMagnet
	if len(req.Data) > 0 {
		var err error
		if hash, err = TorrentHash(req.Data); err != nil {
			return "", err
		}
		operation = OpAddFile
	} else {
		var ok bool
		if hash, ok = MagnetHash(req.Magnet); !ok {
			return "", QbitError("the magnet link has no v1 info hash")
		}
	}
	qb, err := set.Qbit()
	if err != nil {
		return "", err
	}
	resource, err := qbitKey(set, hash)
	if err != nil {
		return "", err
	}
	key := req.OperationKey
	if key == "" {
		key = "media:add:" + ledger.NewKey()
	}
	action := map[string]any{"hash": hash, "tags": req.Tags}
	if req.Magnet != "" {
		action["magnet"] = req.Magnet
	}
	if len(req.Data) > 0 {
		action["name"] = req.Name
	}
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: "media", Operation: operation, OperationKey: key, Target: hash,
		ProposalID: req.Scope.ProposalID, ThreadID: req.Scope.ThreadID, RunID: req.Scope.RunID,
		Action: jsonOf(action), Undo: jsonOf(torrentUndo{Op: UndoTorrent, Hash: hash}),
		Resources: []ledger.Resource{{Key: resource}},
	}, nil)
	if err != nil {
		return "", err
	}
	if len(req.Data) > 0 {
		err = qb.AddFile(ctx, req.Data, req.Name, req.Tags)
	} else {
		err = qb.AddMagnet(ctx, req.Magnet, req.Tags)
	}
	if err != nil {
		_ = store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: settleState(err), Target: hash, Error: err.Error()}, nil)
		return "", err
	}
	if err := store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: hash, After: jsonOf(action),
	}, req.Done); err != nil {
		return "", err
	}
	return hash, nil
}

func (t Tuning) command(ctx context.Context, store *ledger.Store, set Settings, scope Scope, hash, operation string, action any, run func() error) error {
	resource, err := qbitKey(set, hash)
	if err != nil {
		return err
	}
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: "media", Operation: operation, OperationKey: "media:" + operation + ":" + ledger.NewKey(),
		ProposalID: scope.ProposalID, ThreadID: scope.ThreadID, RunID: scope.RunID,
		Target: hash, Action: jsonOf(action), Resources: []ledger.Resource{{Key: resource}},
	}, nil)
	if err != nil {
		return err
	}
	if err := run(); err != nil {
		_ = store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: settleState(err), Target: hash, Error: err.Error()}, nil)
		return err
	}
	return store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, Target: hash, After: jsonOf(action)}, nil)
}

func (t Tuning) SetPriority(ctx context.Context, store *ledger.Store, set Settings, scope Scope, hash string, ids []int, priority int) error {
	qb, err := set.Qbit()
	if err != nil {
		return err
	}
	return t.command(ctx, store, set, scope, hash, OpPriority, map[string]any{"hash": hash, "ids": ids, "priority": priority}, func() error {
		return qb.SetPriority(ctx, hash, ids, priority)
	})
}

func (t Tuning) Tag(ctx context.Context, store *ledger.Store, set Settings, scope Scope, hash, tag string, add bool) error {
	qb, err := set.Qbit()
	if err != nil {
		return err
	}
	operation := OpTagRemove
	action := map[string]any{"hash": hash, "tag": tag}
	if add {
		operation = OpTagAdd
	}
	return t.command(ctx, store, set, scope, hash, operation, action, func() error {
		if add {
			return qb.AddTags(ctx, hash, tag)
		}
		return qb.RemoveTags(ctx, hash, tag)
	})
}

func (t Tuning) Start(ctx context.Context, store *ledger.Store, set Settings, scope Scope, hash string) error {
	qb, err := set.Qbit()
	if err != nil {
		return err
	}
	return t.command(ctx, store, set, scope, hash, OpStart, map[string]any{"hash": hash}, func() error {
		return qb.Start(ctx, hash)
	})
}

func (t Tuning) Delete(ctx context.Context, store *ledger.Store, set Settings, scope Scope, hash string, files bool) error {
	qb, err := set.Qbit()
	if err != nil {
		return err
	}
	return t.command(ctx, store, set, scope, hash, OpDelete, map[string]any{"hash": hash, "files": files}, func() error {
		return qb.Delete(ctx, hash, files)
	})
}

func undoTorrent(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: validateOp(UndoTorrent),
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u torrentUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Hash == "" {
				return ledger.Result{}, ledger.Refused("media undo payload is invalid")
			}
			settings, err := Load(ctx, store.DB.Read, env)
			if err != nil {
				return ledger.Result{}, err
			}
			qb, err := settings.Qbit()
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			found, err := qb.Torrent(ctx, u.Hash)
			if err == nil {
				switch {
				case found == nil:
					return ledger.Result{State: ledger.Failed, Error: "the torrent is no longer in qBittorrent"}, nil
				case found.HasTag(TagFiled):
					return ledger.Result{State: ledger.Failed, Error: "the torrent was already filed into the library"}, nil
				}
				err = qb.Delete(ctx, u.Hash, true)
			}
			if err != nil {
				return ledger.Result{State: settleState(err), Error: err.Error()}, nil
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"deleted": u.Hash})}, nil
		},
	}
}

type agentUndo struct {
	Op         string `json:"op"`
	AgentID    string `json:"agent_id"`
	ForwardKey string `json:"forward_key"`
}

type AgentAction struct {
	Item       string              `json:"item"`
	Key        string              `json:"key"`
	AgentID    string              `json:"agent_id"`
	PlanDigest string              `json:"plan_digest"`
	Plan       protocol.FilingPlan `json:"plan"`
}

func AgentUndo(agentID, key string) json.RawMessage {
	return jsonOf(agentUndo{Op: UndoAgentUnfile, AgentID: agentID, ForwardKey: key})
}

func FilingState(r protocol.FilingResult, err error) (ledger.State, string) {
	if err != nil {
		var e *agentclient.Error
		if errors.As(err, &e) {
			switch e.Mutation {
			case protocol.MutationNone:
				return ledger.Failed, "the media agent refused the filing (" + e.Code + ")"
			case protocol.MutationPartial:
				return ledger.Partial, "the media agent completed part of the filing (" + e.Code + ")"
			}
		}
		return ledger.Unknown, "the media agent did not report a result"
	}
	switch r.State {
	case "done":
		return ledger.Done, ""
	case "failed":
		return ledger.Failed, "the media agent could not file (" + r.ErrorCode + ")"
	case "partial":
		return ledger.Partial, "the media agent completed part of the filing (" + r.ErrorCode + ")"
	}
	return ledger.Unknown, "the media agent could not confirm the result"
}

func undoAgent(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: validateOp(UndoAgentUnfile),
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u agentUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.ForwardKey == "" || u.AgentID == "" {
				return ledger.Result{}, ledger.Refused("media undo payload is invalid")
			}
			settings, err := Load(ctx, store.DB.Read, env)
			if err != nil {
				return ledger.Result{}, err
			}
			agent, err := settings.Agent()
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			health, err := agent.Health(ctx)
			if err != nil {
				return ledger.Result{}, ledger.Refused("the media agent is unavailable")
			}
			if health.AgentID != u.AgentID {
				return ledger.Result{}, ledger.Refused("the media agent changed since this filing; inspect the files and resolve this effect manually")
			}
			res, err := agent.File(ctx, protocol.FilingRequest{Key: protocol.UndoKey(u.ForwardKey), UndoOf: u.ForwardKey})
			var e *agentclient.Error
			if errors.As(err, &e) && e.Mutation == protocol.MutationNone && e.Code == protocol.CodeBusy {
				return ledger.Result{}, ledger.Refused("the media agent has no confirmed result for this filing because it was resolved by hand; undo it by hand")
			}
			if errors.As(err, &e) && e.Mutation == protocol.MutationNone && e.Status != 503 {
				return ledger.Result{}, ledger.Refused("the media agent cannot undo this filing (" + e.Code + ")")
			}
			state, text := FilingState(res, err)
			return ledger.Result{State: state, After: jsonOf(res), Error: text}, nil
		},
	}
}

func addResolve(store *ledger.Store) ledger.ResolveHook {
	return func(ctx context.Context, tx *sql.Tx, e ledger.Effect, r ledger.Resolution) error {
		if r.Outcome != "applied" && r.Outcome != "partial" {
			return nil
		}
		var evidence struct {
			Hash  string `json:"hash"`
			Owned bool   `json:"owned"`
		}
		if len(r.Evidence) == 0 || json.Unmarshal(r.Evidence, &evidence) != nil || evidence.Hash == "" || !evidence.Owned {
			return ledger.Refused("resolving a media add needs evidence with the torrent hash and owned=true")
		}
		if !strings.EqualFold(evidence.Hash, e.Target) {
			return ledger.Refused("the evidence hash does not match the attempted add")
		}
		_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO media_seen (hash, ts) VALUES (?, ?)", strings.ToLower(e.Target), store.Now())
		return err
	}
}

func fileResolve(_ context.Context, _ *sql.Tx, e ledger.Effect, r ledger.Resolution) error {
	if r.Outcome == "not-applied" {
		return nil
	}
	paths := map[string]bool{}
	var action AgentAction
	if json.Unmarshal(e.Action, &action) == nil {
		for _, s := range action.Plan.Steps {
			paths[s.Destination.Root+"/"+s.Destination.Rel] = true
		}
	}
	var legacy struct {
		Step struct {
			Rel string `json:"rel"`
		} `json:"step"`
	}
	if json.Unmarshal(e.Undo, &legacy) == nil && legacy.Step.Rel != "" {
		paths[legacy.Step.Rel] = true
	}
	if len(paths) == 0 {
		return ledger.Refused("the filing has no precise placement to confirm")
	}
	var evidence struct {
		Confirmed []string `json:"confirmed"`
	}
	if len(r.Evidence) == 0 || json.Unmarshal(r.Evidence, &evidence) != nil || len(evidence.Confirmed) == 0 {
		return ledger.Refused("resolving a media filing needs evidence naming the confirmed paths")
	}
	for _, p := range evidence.Confirmed {
		if !paths[p] {
			return ledger.Refused("the evidence names a path the filing did not plan")
		}
	}
	return nil
}

func Undo(s *ledger.Store, env config.Env) []ledger.UndoDefinition {
	return []ledger.UndoDefinition{
		{Operation: UndoTorrent, Undoer: undoTorrent(s, env)},
		{Operation: UndoAgentUnfile, Undoer: undoAgent(s, env)},
		{Operation: UndoFilePlace, Undoer: ledger.Undoer{
			Validate: validateOp(UndoFilePlace),
			External: func(context.Context, json.RawMessage) (ledger.Result, error) {
				return ledger.Result{}, ledger.Refused("filed before the media agent; inspect the files and resolve this effect manually")
			},
		}},
	}
}

func Resolve(s *ledger.Store) []ledger.ResolveDefinition {
	return []ledger.ResolveDefinition{
		{Operation: OpAddMagnet, Hook: addResolve(s)},
		{Operation: OpAddFile, Hook: addResolve(s)},
		{Operation: OpFile, Hook: fileResolve},
		{Operation: OpImportCleanup, Hook: fileResolve},
	}
}
