package torrents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	OpAddMagnet = contract.OpAddMagnet
	OpAddFile   = contract.OpAddFile
	OpPriority  = "media_file_priority"
	OpTagAdd    = "media_tag_add"
	OpTagRemove = "media_tag_remove"
	OpStart     = "media_start"
	OpStop      = "media_stop"
	OpDelete    = "media_delete"

	UndoTorrent = contract.UndoTorrent

	origin = "media"
)

type App struct {
	consumers []contract.Consumer
}

func New(consumers ...contract.Consumer) *App {
	return &App{consumers: consumers}
}

func (a *App) Consumer(label string) (contract.Consumer, bool) {
	for _, c := range a.consumers {
		if c.Label == label {
			return c, true
		}
	}
	return contract.Consumer{}, false
}

func (a *App) Consumers() []contract.Consumer {
	return a.consumers
}

func (a *App) Intake(d contract.Deps) contract.Intake {
	return &intake{app: a, d: d}
}

type intake struct {
	app *App
	d   contract.Deps
}

type torrentUndo struct {
	Op   string `json:"op"`
	Hash string `json:"hash"`
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func openLease(ctx context.Context, q db.Querier) (*connections.Lease, error) {
	id := connections.ID(contract.Kind)
	var (
		lease *connections.Lease
		err   error
	)
	if store := connections.DefaultFor(q); store != nil {
		lease, err = store.Open(ctx, id)
	} else {
		lease, err = connections.OpenRead(ctx, q, config.From(ctx).Host(), id)
	}
	if _, unknown := errors.AsType[connections.UnknownID](err); unknown {
		return nil, config.NotConfigured("the qBittorrent connection is not configured")
	}
	return lease, err
}

func (in *intake) client(ctx context.Context) (*Qbit, string, error) {
	q := in.d.DB.Read
	lease, err := openLease(ctx, q)
	if err != nil {
		return nil, "", err
	}
	base, err := lease.Endpoint("url")
	if err != nil {
		return nil, "", config.NotConfigured("the qBittorrent connection has no URL")
	}
	client, err := lease.HTTP("url")
	if err != nil {
		return nil, "", err
	}
	token, _ := lease.Credential("api_key")
	qb := NewQbit(base, token, client)
	if store := connections.DefaultFor(q); store != nil {
		qb.report = func(ctx context.Context, state connections.State, reason string) {
			_ = store.Observe(context.WithoutCancel(ctx), lease.ID(), lease.Revision(), connections.Result{
				Capability: "api", State: state, Reason: reason,
			})
		}
	}
	return qb, "qbit:" + strings.TrimRight(base, "/") + ":", nil
}

func (in *intake) Ready(ctx context.Context) error {
	_, _, err := in.client(ctx)
	return err
}

func (in *intake) List(ctx context.Context, f contract.Filter) ([]Torrent, error) {
	qb, _, err := in.client(ctx)
	if err != nil {
		return nil, err
	}
	filter := ""
	if f.Completed {
		filter = "completed"
	}
	return qb.Torrents(ctx, filter, f.Label, "")
}

func (in *intake) Get(ctx context.Context, hash string) (*Torrent, error) {
	qb, _, err := in.client(ctx)
	if err != nil {
		return nil, err
	}
	return qb.Torrent(ctx, hash)
}

func (in *intake) Files(ctx context.Context, hash string) ([]TorrentFile, error) {
	qb, _, err := in.client(ctx)
	if err != nil {
		return nil, err
	}
	return qb.Files(ctx, hash)
}

func settleState(err error) ledger.State {
	if contract.QbitDefinite(err) {
		return ledger.Failed
	}
	return ledger.Unknown
}

func actorOf(scope contract.Scope) contract.Actor {
	if scope.Actor == "" {
		return contract.Job
	}
	return scope.Actor
}

func (in *intake) Add(ctx context.Context, req contract.AddRequest) (string, error) {
	hash := ""
	operation := OpAddMagnet
	if len(req.Data) > 0 {
		var err error
		if hash, err = TorrentHash(req.Data); err != nil {
			return "", contract.Invalid(err.Error())
		}
		operation = OpAddFile
	} else {
		var ok bool
		if hash, ok = MagnetHash(req.Magnet); !ok {
			return "", contract.Invalid("the magnet link has no v1 info hash")
		}
	}
	tags := contract.TagAtrium
	if req.Label != "" {
		if _, ok := in.app.Consumer(req.Label); !ok {
			return "", &contract.Refused{Hash: hash, Reason: contract.UnknownLabel}
		}
		tags += "," + req.Label
	}
	qb, prefix, err := in.client(ctx)
	if err != nil {
		return "", err
	}
	key := req.OperationKey
	if key == "" {
		key = "media:add:" + ledger.NewKey()
	}
	action := map[string]any{"hash": hash, "tags": tags, "actor": actorOf(req.Scope)}
	if req.Magnet != "" {
		action["magnet"] = req.Magnet
	}
	if len(req.Data) > 0 {
		action["name"] = req.Name
	}
	store := in.d.Ledger
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: origin, Operation: operation, OperationKey: key, Target: hash,
		ProposalID: req.Scope.ProposalID, ThreadID: req.Scope.ThreadID, RunID: req.Scope.RunID,
		Action: jsonOf(action), Undo: jsonOf(torrentUndo{Op: UndoTorrent, Hash: hash}),
		Resources: []ledger.Resource{{Key: prefix + strings.ToLower(hash)}},
	}, nil)
	if err != nil {
		return "", err
	}
	if len(req.Data) > 0 {
		err = qb.AddFile(ctx, req.Data, req.Name, tags)
	} else {
		err = qb.AddMagnet(ctx, req.Magnet, tags)
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

func (in *intake) gate(ctx context.Context, qb *Qbit, scope contract.Scope, hash, label string) (*Torrent, error) {
	if label != "" {
		if _, ok := in.app.Consumer(label); !ok {
			return nil, &contract.Refused{Hash: hash, Reason: contract.UnknownLabel}
		}
	}
	actor := actorOf(scope)
	if actor == contract.Operator {
		return nil, nil
	}
	found, err := qb.Torrent(ctx, hash)
	switch {
	case err != nil:
		return nil, err
	case found == nil:
		return nil, &contract.Refused{Hash: hash, Reason: contract.Missing}
	case !found.Provenance():
		return nil, &contract.Refused{Hash: hash, Reason: contract.Untagged}
	case actor == contract.Job && label != "" && !found.HasTag(label):
		return nil, &contract.Refused{Hash: hash, Reason: contract.Unlabelled}
	}
	return found, nil
}

func (in *intake) command(ctx context.Context, scope contract.Scope, hash, label, operation string, action map[string]any, check func(*Torrent) error, run func(*Qbit) error) error {
	qb, prefix, err := in.client(ctx)
	if err != nil {
		return err
	}
	found, err := in.gate(ctx, qb, scope, hash, label)
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(found); err != nil {
			return err
		}
	}
	action["actor"] = actorOf(scope)
	store := in.d.Ledger
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: origin, Operation: operation, OperationKey: "media:" + operation + ":" + ledger.NewKey(),
		ProposalID: scope.ProposalID, ThreadID: scope.ThreadID, RunID: scope.RunID,
		Target: hash, Action: jsonOf(action), Resources: []ledger.Resource{{Key: prefix + strings.ToLower(hash)}},
	}, nil)
	if err != nil {
		return err
	}
	if err := run(qb); err != nil {
		_ = store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: settleState(err), Target: hash, Error: err.Error()}, nil)
		return err
	}
	return store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, Target: hash, After: jsonOf(action)}, nil)
}

func (in *intake) SetWanted(ctx context.Context, scope contract.Scope, hash, label string, ids []int, wanted bool) error {
	priority := contract.PrioritySkip
	if wanted {
		priority = contract.PriorityNormal
	}
	return in.command(ctx, scope, hash, label, OpPriority, map[string]any{"hash": hash, "ids": ids, "priority": priority}, nil, func(qb *Qbit) error {
		return qb.SetPriority(ctx, hash, ids, priority)
	})
}

func (in *intake) protected(mark string) bool {
	if mark == contract.TagAtrium || mark == contract.TagLegacy {
		return true
	}
	_, ok := in.app.Consumer(mark)
	return ok
}

func (in *intake) Mark(ctx context.Context, scope contract.Scope, hash, label, mark string, on bool) error {
	if !on && in.protected(mark) {
		return &contract.Refused{Hash: hash, Reason: contract.Protected}
	}
	operation := OpTagRemove
	if on {
		operation = OpTagAdd
	}
	return in.command(ctx, scope, hash, label, operation, map[string]any{"hash": hash, "tag": mark}, nil, func(qb *Qbit) error {
		if on {
			return qb.AddTags(ctx, hash, mark)
		}
		return qb.RemoveTags(ctx, hash, mark)
	})
}

func (in *intake) Control(ctx context.Context, c contract.Control) error {
	action := map[string]any{"hash": c.Hash}
	switch c.Action {
	case contract.Pause:
		return in.command(ctx, c.Scope, c.Hash, c.Label, OpStop, action, nil, func(qb *Qbit) error { return qb.Stop(ctx, c.Hash) })
	case contract.Resume:
		return in.command(ctx, c.Scope, c.Hash, c.Label, OpStart, action, nil, func(qb *Qbit) error { return qb.Start(ctx, c.Hash) })
	case contract.Remove:
		action["files"] = c.DeleteFiles
		check := func(found *Torrent) error {
			if actorOf(c.Scope) != contract.Job {
				return nil
			}
			consumer, ok := in.app.Consumer(c.Label)
			switch {
			case !ok:
				return &contract.Refused{Hash: c.Hash, Reason: contract.Unlabelled}
			case !found.HasTag(consumer.Consumed):
				return &contract.Refused{Hash: c.Hash, Reason: contract.NotConsumed}
			case !found.Stopped():
				return &contract.Refused{Hash: c.Hash, Reason: contract.NotStopped}
			}
			return nil
		}
		return in.command(ctx, c.Scope, c.Hash, c.Label, OpDelete, action, check, func(qb *Qbit) error { return qb.Delete(ctx, c.Hash, c.DeleteFiles) })
	}
	return contract.Invalid("unknown torrent action " + string(c.Action))
}

func validateUndo(raw json.RawMessage) error {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return ledger.Refused("media undo payload is invalid")
	}
	if head.Op != UndoTorrent {
		return ledger.Refused("media undo payload names " + head.Op)
	}
	return nil
}

func (a *App) Undo(store *ledger.Store, env config.Env) []ledger.UndoDefinition {
	return []ledger.UndoDefinition{{Operation: UndoTorrent, Undoer: ledger.Undoer{
		Validate: validateUndo,
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u torrentUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Hash == "" {
				return ledger.Result{}, ledger.Refused("media undo payload is invalid")
			}
			in := &intake{app: a, d: contract.Deps{DB: store.DB, Env: env, Ledger: store}}
			qb, _, err := in.client(ctx)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			found, err := qb.Torrent(ctx, u.Hash)
			if err == nil {
				if found == nil {
					return ledger.Result{State: ledger.Failed, Error: "the torrent is no longer in qBittorrent"}, nil
				}
				for _, c := range a.consumers {
					if found.HasTag(c.Consumed) {
						return ledger.Result{State: ledger.Failed, Error: "the torrent was already " + c.Consumed + " into " + c.Title}, nil
					}
				}
				err = qb.Delete(ctx, u.Hash, true)
			}
			if err != nil {
				return ledger.Result{State: settleState(err), Error: err.Error()}, nil
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"deleted": u.Hash})}, nil
		},
	}}}
}
