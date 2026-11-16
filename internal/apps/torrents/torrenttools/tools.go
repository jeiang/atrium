package torrenttools

import (
	"context"
	"encoding/json"
	"math"

	"github.com/jeiang/atrium/internal/apps/torrents"
	torrentsapp "github.com/jeiang/atrium/internal/apps/torrents/application"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

const (
	NameDownloads = "downloads"
	NameAdd       = "download_add"
	NameControl   = "download_control"
)

var (
	readKinds  = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}
	writeKinds = tool.Kinds{tool.Chat}
)

type handlers struct {
	app *torrents.App
}

func (h handlers) service(tc *tool.Context) *torrentsapp.Service {
	return torrentsapp.New(torrentsapp.Deps{DB: tc.DB, Env: tc.Env, Ledger: tc.Ledger, App: h.app, Actor: contract.Agent})
}

func Definitions(app *torrents.App) []module.ToolDefinition {
	tools := Tools(app)
	defs := make([]module.ToolDefinition, len(tools))
	for i, t := range tools {
		defs[i] = module.ToolDefinition{Tool: t}
	}
	return defs
}

func Tools(app *torrents.App) []tool.Tool {
	h := handlers{app: app}
	return []tool.Tool{
		{
			Name:        NameDownloads,
			Description: "List torrents in qBittorrent (hash, name, state, progress, size, speed, ETA, media or plain, and whether the library has filed it).",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}, "additionalProperties": false}`),
			Handler:     h.downloads,
			Effect:      tool.Read,
			Annotated:   true,
			Contexts:    readKinds,
		},
		asstapp.ApprovalGate(tool.Tool{
			Name:        NameAdd,
			Description: "Ask the operator to approve adding a magnet link to qBittorrent; nothing is added until they approve the card. Every add is tagged atrium. media=true (default) also labels it for filing into the library with automatic video/subtitle selection; false adds a plain torrent. Undo deletes the torrent and its data unless it was already filed.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"magnet": {"type": "string"}, "media": {"type": "boolean"}}, "required": ["magnet"], "additionalProperties": false}`),
			Effect:      tool.Propose, Undo: torrents.UndoTorrent,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  writeKinds,
			Approval:  &tool.Approval{Apply: h.add, Check: h.checkAdd, Label: "Add torrent", Icon: "download", Module: unit.Torrents},
		}),
		asstapp.ApprovalGate(tool.Tool{
			Name:        NameControl,
			Description: "Ask the operator to approve pausing, resuming or removing a torrent by hash (see downloads); nothing changes until they approve the card. Only torrents tagged atrium (or the older media tag) can be changed. remove keeps the downloaded data unless delete_files is true. There is no undo.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {"hash": {"type": "string"}, "action": {"type": "string", "enum": ["pause", "resume", "remove"]}, "delete_files": {"type": "boolean"}}, "required": ["hash", "action"], "additionalProperties": false}`),
			Effect:      tool.Propose,
			Annotated:   true,
			Channel:     tool.ChannelOwn,
			Contexts:    writeKinds,
			Approval:    &tool.Approval{Apply: h.control, Check: h.checkControl, Label: "Change download", Icon: "download", Module: unit.Torrents, Detail: "action"},
		}),
	}
}

func optionalInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func boolArg(args tool.Args, name string, def bool) (bool, error) {
	value, ok := args.Get(name)
	if !ok {
		return def, nil
	}
	b, isBool := value.(bool)
	if !isBool {
		return false, tool.Error(name + " must be true or false")
	}
	return b, nil
}

func (h handlers) torrentDict(t contract.Torrent) map[string]any {
	state := t.State
	if label, ok := contract.StateLabels[t.State]; ok {
		state = label
	}
	kind := "plain"
	if t.HasTag(contract.TagLegacy) {
		kind = "media"
	}
	filed := false
	if consumer, ok := h.app.Consumer(contract.TagLegacy); ok {
		filed = t.HasTag(consumer.Consumed)
	}
	return map[string]any{"hash": t.Hash, "name": t.Name, "state": state, "progress_percent": int64(math.RoundToEven(t.Progress * 100)), "size_bytes": optionalInt(t.Size),
		"download_bytes_per_second": optionalInt(t.DLSpeed), "eta_seconds": optionalInt(t.ETA), "kind": kind, "atrium": t.Provenance(), "filed": filed}
}

func (h handlers) downloads(ctx context.Context, tc *tool.Context, _ tool.Args) (any, error) {
	read, err := h.service(tc).Downloads(ctx, torrentsapp.DownloadsInput{})
	if err != nil {
		return nil, tool.Fail(err)
	}
	if read.Data.TorrentsError != nil {
		return nil, tool.Fail(read.Data.TorrentsError)
	}
	found := read.Data.Torrents
	shown := []any{}
	for _, t := range found[:min(asstapp.TuningAgentMediaTorrentLimit, len(found))] {
		shown = append(shown, h.torrentDict(t))
	}
	return map[string]any{"torrents": shown, "note": "Only torrents of kind media are filed into the library, from the Media downloads page."}, nil
}

func addArgs(args tool.Args) (string, bool, error) {
	magnet, _, err := tool.TextArg(args, "magnet", true)
	if err != nil {
		return "", false, err
	}
	asMedia, err := boolArg(args, "media", true)
	if err != nil {
		return "", false, err
	}
	return magnet, asMedia, nil
}

func (h handlers) checkAdd(ctx context.Context, tc *tool.Context, args tool.Args) error {
	magnet, _, err := addArgs(args)
	if err != nil {
		return err
	}
	if err := h.service(tc).CheckMagnet(ctx, magnet); err != nil {
		return tool.Fail(err)
	}
	return nil
}

func (h handlers) add(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	magnet, asMedia, err := addArgs(args)
	if err != nil {
		return nil, err
	}
	added, err := h.service(tc).Add(ctx, torrentsapp.AddInput{Magnet: &torrentsapp.Magnet{URI: magnet}, Media: asMedia, ProposalID: tc.ProposalID})
	if err != nil {
		return nil, tool.Fail(err)
	}
	return map[string]any{"hash": added.Data.Hash, "kind": added.Data.Kind}, nil
}

func controlArgs(args tool.Args) (torrentsapp.ControlInput, error) {
	hash, _, err := tool.TextArg(args, "hash", true)
	if err != nil {
		return torrentsapp.ControlInput{}, err
	}
	action, _, err := tool.TextArg(args, "action", true)
	if err != nil {
		return torrentsapp.ControlInput{}, err
	}
	switch contract.Action(action) {
	case contract.Pause, contract.Resume, contract.Remove:
	default:
		return torrentsapp.ControlInput{}, tool.Error("action must be pause, resume or remove")
	}
	files, err := boolArg(args, "delete_files", false)
	if err != nil {
		return torrentsapp.ControlInput{}, err
	}
	return torrentsapp.ControlInput{Hash: hash, Action: contract.Action(action), DeleteFiles: files}, nil
}

func (h handlers) checkControl(_ context.Context, _ *tool.Context, args tool.Args) error {
	_, err := controlArgs(args)
	return err
}

func (h handlers) control(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	in, err := controlArgs(args)
	if err != nil {
		return nil, err
	}
	in.ProposalID = tc.ProposalID
	done, err := h.service(tc).Control(ctx, in)
	if err != nil {
		return nil, tool.Fail(err)
	}
	return map[string]any{"hash": done.Data.Hash, "action": done.Data.Action}, nil
}
