package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	OpAddMagnet = "media_add_magnet"
	OpAddFile   = "media_add_file"
	OpPriority  = "media_file_priority"
	OpTagAdd    = "media_tag_add"
	OpTagRemove = "media_tag_remove"
	OpStart     = "media_start"
	OpDelete    = "media_delete"
	OpFile      = "media_file"

	UndoTorrent   = "media_torrent_delete"
	UndoFilePlace = "media_file_place"
)

type torrentUndo struct {
	Op   string `json:"op"`
	Hash string `json:"hash"`
}

type fileStep struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Rel  string `json:"rel"`
	Link bool   `json:"link"`
	Dev  uint64 `json:"dev"`
	Ino  uint64 `json:"ino"`
	Size int64  `json:"size"`
}

type filePlan struct {
	Item    string     `json:"item"`
	Torrent bool       `json:"torrent"`
	Root    string     `json:"root"`
	Steps   []fileStep `json:"steps"`
}

type fileUndo struct {
	Op   string   `json:"op"`
	Step fileStep `json:"step"`
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

func qbitKey(set Settings, hash string) string {
	return "qbit:" + strings.TrimRight(set.QbitURL, "/") + ":" + strings.ToLower(hash)
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
		Resources: []ledger.Resource{{Key: qbitKey(set, hash)}},
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
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: "media", Operation: operation, OperationKey: "media:" + operation + ":" + ledger.NewKey(),
		ProposalID: scope.ProposalID, ThreadID: scope.ThreadID, RunID: scope.RunID,
		Target: hash, Action: jsonOf(action), Resources: []ledger.Resource{{Key: qbitKey(set, hash)}},
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

func place(step fileStep) error {
	if err := os.MkdirAll(filepath.Dir(step.Dst), dirMode); err != nil {
		return err
	}
	if step.Link {
		return os.Link(step.Src, step.Dst)
	}
	return os.Rename(step.Src, step.Dst)
}

func sameIdentity(step fileStep, info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(st.Dev) == step.Dev && st.Ino == step.Ino && info.Size() == step.Size
}

func undoFilePlace(_ context.Context, raw json.RawMessage) (ledger.Result, error) {
	var u fileUndo
	if err := json.Unmarshal(raw, &u); err != nil || u.Step.Dst == "" {
		return ledger.Result{}, ledger.Refused("media file undo payload is invalid")
	}
	step := u.Step
	info, err := os.Lstat(step.Dst)
	if errors.Is(err, os.ErrNotExist) {
		return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"missing": step.Rel})}, nil
	}
	if err != nil {
		return ledger.Result{State: ledger.Unknown, Error: err.Error()}, nil
	}
	if !sameIdentity(step, info) {
		return ledger.Result{}, ledger.Refused("the destination changed since it was filed; not removing it")
	}
	if step.Link {
		src, err := os.Stat(step.Src)
		if err != nil || !os.SameFile(src, info) {
			return ledger.Result{}, ledger.Refused("the filed link no longer matches its download; not removing it")
		}
		if err := os.Remove(step.Dst); err != nil {
			return ledger.Result{State: ledger.Unknown, Error: err.Error()}, nil
		}
		return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"removed": step.Rel})}, nil
	}
	if _, err := os.Lstat(step.Src); err == nil {
		return ledger.Result{}, ledger.Refused("the import source exists again; not overwriting it")
	}
	if err := os.MkdirAll(filepath.Dir(step.Src), dirMode); err != nil {
		return ledger.Result{State: ledger.Unknown, Error: err.Error()}, nil
	}
	if err := os.Rename(step.Dst, step.Src); err != nil {
		return ledger.Result{State: ledger.Unknown, Error: err.Error()}, nil
	}
	return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"restored": step.Rel})}, nil
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
	var u fileUndo
	if err := json.Unmarshal(e.Undo, &u); err != nil || u.Step.Rel == "" {
		return ledger.Refused("the filing has no precise placement to confirm")
	}
	var evidence struct {
		Confirmed []string `json:"confirmed"`
	}
	if len(r.Evidence) == 0 || json.Unmarshal(r.Evidence, &evidence) != nil ||
		len(evidence.Confirmed) != 1 || evidence.Confirmed[0] != u.Step.Rel {
		return ledger.Refused("resolving a media filing needs evidence naming the confirmed path " + u.Step.Rel)
	}
	return nil
}

func RegisterLedgerUndoer(s *ledger.Store, env config.Env) error {
	if err := s.RegisterUndo(UndoTorrent, undoTorrent(s, env)); err != nil {
		return err
	}
	if err := s.RegisterUndo(UndoFilePlace, ledger.Undoer{
		Validate: validateOp(UndoFilePlace),
		External: undoFilePlace,
	}); err != nil {
		return err
	}
	for _, operation := range []string{OpAddMagnet, OpAddFile} {
		if err := s.RegisterResolve(operation, addResolve(s)); err != nil {
			return err
		}
	}
	return s.RegisterResolve(OpFile, fileResolve)
}
