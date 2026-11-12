package export

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/kb/render"
	"github.com/jeiang/atrium/internal/ledger"
)

const TargetObsidian = "obsidian"

func runObsidian(ctx context.Context, d *db.DB, env config.Env, now float64) (Result, error) {
	dir, err := readSetting(ctx, d, ObsidianDirKey)
	if err != nil {
		return Result{}, err
	}
	dir = filepath.Clean(dir)
	privateTo, err := readSetting(ctx, d, ObsidianPrivateKey)
	if err != nil {
		return Result{}, err
	}
	allow, err := readSetting(ctx, d, ObsidianKindsKey)
	if err != nil {
		return Result{}, err
	}
	records, err := kb.Exportable(ctx, d.Read, kb.ExportFilter{
		Now:            now,
		Kinds:          mask(allow),
		IncludePrivate: privateTo != "" && filepath.Clean(privateTo) == dir,
	})
	if err != nil {
		return Result{}, err
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return Result{}, err
	}
	base := config.From(ctx).PublicURL(env)
	titles, err := threadTitles(ctx, d.Read, records)
	if err != nil {
		return Result{}, err
	}
	entries := make([]pageEntry, 0, len(records)+1)
	for _, rec := range records {
		src := render.Source{PublicURL: base, Date: date(rec.UpdatedAt, loc)}
		if rec.SourceThreadID != nil {
			src.ThreadTitle = titles[*rec.SourceThreadID]
		}
		entries = append(entries, pageEntry{
			recordID: rec.ID,
			page:     render.RecordPage(rec.Record, src),
			path:     filepath.Join(rec.Scope, slug(rec.Title)+"-"+kb.Cite(rec.ID)+".md"),
			front:    frontMatter(rec, src),
		})
	}
	entries = append(entries, pageEntry{recordID: 0, page: render.Index(indexEntries(records, loc)), path: "index.md"})
	store, err := exportLedger(ctx, d, env)
	if err != nil {
		return Result{}, err
	}
	return syncObsidian(ctx, store, now, dir, entries)
}

func syncObsidian(ctx context.Context, store *ledger.Store, now float64, dir string, entries []pageEntry) (Result, error) {
	var out Result
	d := store.DB
	if err := blockedPrefix(ctx, d.Read, "obsidian:"); err != nil {
		return out, err
	}
	stored, err := loadPages(ctx, d.Read, TargetObsidian)
	if err != nil {
		return out, err
	}
	current := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		current[entry.recordID] = true
		prior, found := stored[entry.recordID]
		if found && prior.hash == entry.page.Hash && prior.remoteID == entry.path {
			out.Skipped++
			continue
		}
		full, err := within(dir, entry.path)
		if err != nil {
			return out, err
		}
		content := []byte(entry.front + entry.page.Markdown)
		renaming := found && prior.remoteID != entry.path
		var writeDone ledger.Mutation
		if !renaming {
			writeDone = func(ctx context.Context, tx *sql.Tx) error {
				return savePage(ctx, tx, TargetObsidian, entry.recordID, entry.path, entry.page.Hash, now)
			}
		}
		if err := writeStep(ctx, store, entry.recordID, now, full, entry.path, entry.page.Hash, content, writeDone, !renaming); err != nil {
			return out, err
		}
		if renaming {
			oldFull, err := within(dir, prior.remoteID)
			if err != nil {
				return out, err
			}
			if err := removeStep(ctx, store, entry.recordID, now, oldFull, prior.remoteID, prior.hash, entry.path, entry.page.Hash, false, func(ctx context.Context, tx *sql.Tx) error {
				return savePage(ctx, tx, TargetObsidian, entry.recordID, entry.path, entry.page.Hash, now)
			}); err != nil {
				return out, err
			}
		}
		if found {
			out.Updated++
		} else {
			out.Created++
		}
	}
	for recordID, prior := range stored {
		if current[recordID] {
			continue
		}
		full, err := within(dir, prior.remoteID)
		if err != nil {
			return out, err
		}
		if err := removeStep(ctx, store, recordID, now, full, prior.remoteID, prior.hash, "", "", true, func(ctx context.Context, tx *sql.Tx) error {
			return deletePage(ctx, tx, TargetObsidian, recordID)
		}); err != nil {
			return out, err
		}
		out.Removed++
	}
	return out, nil
}

type fileUndo struct {
	Op      string   `json:"op"`
	Path    string   `json:"path"`
	Before  *string  `json:"before"`
	SHA     string   `json:"sha"`
	Missing bool     `json:"missing,omitempty"`
	Parents []string `json:"parents,omitempty"`
}

const UndoObsidianRestore = "obsidian_restore"

func fileSnapshot(full string) (*string, []string, error) {
	prior, err := os.ReadFile(full)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	var before *string
	if err == nil {
		text := string(prior)
		before = &text
	}
	var parents []string
	if err != nil {
		for parent := filepath.Dir(full); ; parent = filepath.Dir(parent) {
			if _, statErr := os.Stat(parent); statErr == nil {
				break
			}
			parents = append(parents, parent)
			if filepath.Dir(parent) == parent {
				break
			}
		}
	}
	return before, parents, nil
}

func writeStep(ctx context.Context, store *ledger.Store, recordID int64, now float64, full, rel, pageHash string, content []byte, done ledger.Mutation, mapping bool) error {
	before, parents, err := fileSnapshot(full)
	if err != nil {
		return err
	}
	target := "obsidian:" + rel
	contentHash := hashOf(content)
	generation, err := ledger.CountEffectiveDone(ctx, store.DB.Read, target)
	if err != nil {
		return err
	}
	action := map[string]any{"hash": pageHash, "content_hash": contentHash, "path": rel, "record_id": recordID, "mapping": mapping}
	undo := fileUndo{Op: UndoObsidianRestore, Path: full, Before: before, SHA: contentHash, Parents: parents}
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: "export", Operation: "obsidian_write",
		OperationKey: "export:obsidian:" + cite(recordID) + ":" + strconv.Itoa(generation) + ":write:" + rel + ":" + contentHash,
		Target:       target, Before: jsonOf(before), Action: jsonOf(action), Undo: jsonOf(undo),
		Resources: []ledger.Resource{{Key: "file:" + full}},
	}, nil)
	if err != nil {
		return err
	}
	if err := writePath(full, content); err != nil {
		_ = store.Settle(ctx, ticket, ledger.Result{State: ledger.Failed, Target: target, Error: err.Error()}, nil)
		return err
	}
	return store.Settle(ctx, ticket, ledger.Result{State: ledger.Done, Target: target, After: jsonOf(action)}, done)
}

func removeStep(ctx context.Context, store *ledger.Store, recordID int64, now float64, full, rel, priorHash, nextPath, nextHash string, retract bool, done ledger.Mutation) error {
	before, parents, err := fileSnapshot(full)
	if err != nil {
		return err
	}
	target := "obsidian:" + rel
	generation, err := ledger.CountEffectiveDone(ctx, store.DB.Read, target)
	if err != nil {
		return err
	}
	action := map[string]any{"path": rel, "record_id": recordID, "retract": retract}
	if nextPath != "" {
		action["next_path"] = nextPath
		action["next_hash"] = nextHash
	}
	ticket, err := store.Begin(ctx, ledger.New{
		Origin: "export", Operation: "obsidian_remove",
		OperationKey: "export:obsidian:" + cite(recordID) + ":" + strconv.Itoa(generation) + ":remove:" + rel,
		Target:       target, Before: jsonOf(before), Action: jsonOf(action),
		Undo:      jsonOf(fileUndo{Op: UndoObsidianRestore, Path: full, Before: before, Missing: true, Parents: parents}),
		Resources: []ledger.Resource{{Key: "file:" + full}},
	}, nil)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = store.Settle(ctx, ticket, ledger.Result{State: ledger.Failed, Target: target, Error: err.Error()}, nil)
		return err
	}
	if before == nil {
		if err := store.Settle(ctx, ticket, ledger.Result{State: ledger.Skipped, Target: target}, done); err != nil {
			return err
		}
		return nil
	}
	return store.Settle(ctx, ticket, ledger.Result{State: ledger.Done, Target: target, After: jsonOf(map[string]any{"removed": rel, "hash": priorHash})}, done)
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func within(dir, rel string) (string, error) {
	base := filepath.Clean(dir) + string(os.PathSeparator)
	full := filepath.Clean(filepath.Join(dir, rel))
	if !strings.HasPrefix(full, base) {
		return "", fmt.Errorf("export: %s is outside %s", rel, dir)
	}
	return full, nil
}

func writePath(full string, content []byte) error {
	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".atrium-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	remove := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		remove()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		remove()
		return err
	}
	if err := tmp.Close(); err != nil {
		remove()
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		remove()
		return err
	}
	if err := os.Rename(name, full); err != nil {
		remove()
		return err
	}
	return nil
}

func restoreFile(u fileUndo) error {
	current, err := os.ReadFile(u.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if u.Missing {
		if err == nil {
			return ledger.Refused("the path was recreated since the removal; not restoring")
		}
	} else if u.SHA != "" && hashOf(current) != u.SHA {
		return ledger.Refused("the exported file changed since the write; not restoring")
	}
	if u.Before == nil {
		if err := os.Remove(u.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, parent := range u.Parents {
			if err := os.Remove(parent); err != nil {
				break
			}
		}
		return nil
	}
	return writePath(u.Path, []byte(*u.Before))
}

func obsidianResolves(s *ledger.Store) []ledger.ResolveDefinition {
	return []ledger.ResolveDefinition{
		{Operation: "obsidian_write", Hook: func(ctx context.Context, tx *sql.Tx, e ledger.Effect, r ledger.Resolution) error {
			if r.Outcome != "applied" && r.Outcome != "partial" {
				return nil
			}
			var action struct {
				Hash     string `json:"hash"`
				Path     string `json:"path"`
				RecordID int64  `json:"record_id"`
				Mapping  bool   `json:"mapping"`
			}
			if err := json.Unmarshal(e.Action, &action); err != nil {
				return err
			}
			if !action.Mapping || action.Hash == "" || action.Path == "" {
				return nil
			}
			return savePage(ctx, tx, TargetObsidian, action.RecordID, action.Path, action.Hash, s.Now())
		}},
		{Operation: "obsidian_remove", Hook: func(ctx context.Context, tx *sql.Tx, e ledger.Effect, r ledger.Resolution) error {
			if r.Outcome != "applied" && r.Outcome != "partial" {
				return nil
			}
			var action struct {
				RecordID int64  `json:"record_id"`
				Retract  bool   `json:"retract"`
				NextPath string `json:"next_path"`
				NextHash string `json:"next_hash"`
			}
			if err := json.Unmarshal(e.Action, &action); err != nil {
				return err
			}
			if action.Retract {
				return deletePage(ctx, tx, TargetObsidian, action.RecordID)
			}
			if action.NextPath != "" {
				return savePage(ctx, tx, TargetObsidian, action.RecordID, action.NextPath, action.NextHash, s.Now())
			}
			return nil
		}},
	}
}

func obsidianUndo(s *ledger.Store) []ledger.UndoDefinition {
	return []ledger.UndoDefinition{{Operation: UndoObsidianRestore, Undoer: ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var u fileUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Path == "" {
				return ledger.Refused("obsidian_restore without a path")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u fileUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Path == "" {
				return ledger.Result{}, ledger.Refused("obsidian_restore without a path")
			}
			if err := restoreFile(u); err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"restored": u.Path})}, nil
		},
		After: func(ctx context.Context, tx *sql.Tx, e ledger.Effect, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			rel, ok := strings.CutPrefix(e.Target, TargetObsidian+":")
			if !ok {
				return nil
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM export_pages WHERE target = ? AND remote_id = ?", TargetObsidian, rel)
			return err
		},
	}}}
}

func slug(title string) string {
	var b strings.Builder
	last := false
	for _, r := range strings.ToLower(title) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			last = false
			continue
		}
		if !last && b.Len() > 0 {
			b.WriteByte('-')
			last = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.TrimRight(out[:60], "-")
	}
	if out == "" {
		return "record"
	}
	return out
}

func yamlString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func frontMatter(rec kb.ExportRecord, src render.Source) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("atrium_id: " + kb.Cite(rec.ID) + "\n")
	b.WriteString("title: " + yamlString(strings.Join(strings.Fields(rec.Title), " ")) + "\n")
	b.WriteString("scope: " + rec.Scope + "\n")
	b.WriteString("writer: " + rec.Writer + "\n")
	b.WriteString("confirmed: " + strconv.FormatBool(rec.Confirmed()) + "\n")
	b.WriteString("updated: " + src.Date + "\n")
	if url := render.SourceURL(rec.Record, src); url != "" {
		b.WriteString("source: " + yamlString(url) + "\n")
	} else if rec.Origin != nil {
		b.WriteString("source: " + yamlString(*rec.Origin) + "\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}
