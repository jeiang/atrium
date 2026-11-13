package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	FileRestoreOp  = "file_restore"
	fileWriteOp    = "file_write"
	badFileName    = "invalid file name; use a plain name inside the files directory"
	fileWriteGuard = "file:"
)

func fileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !nameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", invalid("name", badFileName, nil)
	}
	return name, nil
}

func (s *Service) threadFilesDir(threadID int64) string {
	return filepath.Join(s.filesDir, files.ThreadOwner(threadID).Dir())
}

func (s *Service) fileRoots(ctx context.Context, threadID int64) ([]string, error) {
	out := []string{s.threadFilesDir(threadID)}
	thread, err := threads.Get(ctx, s.db.Read, threadID)
	if err != nil {
		return nil, err
	}
	if thread != nil && thread.ProjectID != nil && *thread.ProjectID != 0 {
		out = append(out, filepath.Join(s.filesDir, files.ProjectOwner(*thread.ProjectID).Dir()))
	}
	return out, nil
}

func resolveFile(dirs []string, name string) string {
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	return filepath.Join(dirs[0], name)
}

func (s *Service) writeFile(ctx context.Context, in FileWriteInput) (FileChanged, error) {
	name, err := fileName(in.Name)
	if err != nil {
		return FileChanged{}, err
	}
	limits, err := files.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return FileChanged{}, err
	}
	if len(in.Content) > limits.FileCap {
		return FileChanged{}, refusedFile(fmt.Sprintf("content is larger than the %d byte cap", limits.FileCap))
	}
	dir := s.threadFilesDir(in.ThreadID)
	path := filepath.Join(dir, name)
	prior, priorErr := os.ReadFile(path)
	if priorErr != nil && !errors.Is(priorErr, os.ErrNotExist) {
		return FileChanged{}, priorErr
	}
	var before any
	if priorErr == nil {
		if !utf8.Valid(prior) {
			return FileChanged{}, refusedFile(name + " is not a text file")
		}
		before = string(prior)
	}
	var parents []string
	if priorErr != nil {
		for parent := dir; ; parent = filepath.Dir(parent) {
			if _, statErr := os.Stat(parent); statErr == nil {
				break
			}
			parents = append(parents, parent)
			if filepath.Dir(parent) == parent {
				break
			}
		}
	}
	undo := map[string]any{"op": FileRestoreOp, "path": path, "before": before, "sha": hash([]byte(in.Content)), "parents": parents}
	threadID, runID := in.ThreadID, in.RunID
	ticket, err := s.ledger.Begin(ctx, ledger.New{
		Origin: "agent", Operation: fileWriteOp, OperationKey: fileWriteOp + ":" + ledger.NewKey(),
		Target: name, ThreadID: &threadID, RunID: &runID, ProposalID: in.ProposalID,
		Before: jsonOf(before), Undo: jsonOf(undo), Resources: []ledger.Resource{{Key: fileWriteGuard + path}},
	}, nil)
	if err != nil {
		return FileChanged{}, err
	}
	if err := writeAtomic(dir, path, []byte(in.Content)); err != nil {
		_ = s.ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Failed, Target: name, Error: err.Error()}, nil)
		return FileChanged{}, err
	}
	if err := s.ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: name, After: jsonOf(in.Content), Undo: jsonOf(undo),
	}, nil); err != nil {
		return FileChanged{}, err
	}
	return FileChanged{EffectID: ticket.ID}, nil
}

func writeAtomic(dir, path string, content []byte) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".atrium-*.tmp")
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
	if err := os.Chmod(name, 0o666); err != nil {
		remove()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		remove()
		return err
	}
	return nil
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func FileUndo() []ledger.UndoDefinition {
	return []ledger.UndoDefinition{{Operation: FileRestoreOp, Undoer: ledger.Undoer{
		Validate: func(payload json.RawMessage) error {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(payload, &p); err != nil || p.Path == "" {
				return ledger.Refused("file_restore without a path")
			}
			return nil
		},
		External: func(ctx context.Context, payload json.RawMessage) (ledger.Result, error) {
			var p struct {
				Path    string   `json:"path"`
				Before  *string  `json:"before"`
				SHA     string   `json:"sha"`
				Parents []string `json:"parents"`
			}
			if err := json.Unmarshal(payload, &p); err != nil {
				return ledger.Result{}, ledger.Refused("file_restore payload is invalid")
			}
			if p.Path == "" {
				return ledger.Result{}, ledger.Refused("file_restore without a path")
			}
			current, err := os.ReadFile(p.Path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return ledger.Result{}, err
			}
			if hash(current) != p.SHA {
				return ledger.Result{}, ledger.Refused("file changed since the write; not restoring")
			}
			if p.Before == nil {
				if err := os.Remove(p.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return ledger.Result{}, err
				}
				for _, parent := range p.Parents {
					if err := os.Remove(parent); err != nil {
						break
					}
				}
				return ledger.Result{State: ledger.Done, After: jsonOf("removed")}, nil
			}
			if err := writeAtomic(filepath.Dir(p.Path), p.Path, []byte(*p.Before)); err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf("restored")}, nil
		},
	}}}
}
