package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/apps/library/agentclient"
	"github.com/jeiang/atrium/internal/apps/library/mediaagent/protocol"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	OpFile          = "media_file"
	OpImportCleanup = "media_import_cleanup"

	UndoAgentUnfile = "media_agent_unfile"
)

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
			agent, err := settings.Agent(protocol.CapabilityFile)
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
		{Operation: UndoAgentUnfile, Undoer: undoAgent(s, env)},
	}
}

func Resolve(s *ledger.Store) []ledger.ResolveDefinition {
	return []ledger.ResolveDefinition{
		{Operation: contract.OpAddMagnet, Hook: addResolve(s)},
		{Operation: contract.OpAddFile, Hook: addResolve(s)},
		{Operation: OpFile, Hook: fileResolve},
		{Operation: OpImportCleanup, Hook: fileResolve},
	}
}
