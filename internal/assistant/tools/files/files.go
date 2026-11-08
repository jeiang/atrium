package filetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	filestore "github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
)

const UndoOp = "file_restore"

const (
	readName   = "file_read"
	searchName = "file_search"
	writeName  = "file_write"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const readSchema = `{"type":"object","properties":{"name":{"type":"string","maxLength":200,"description":"File name inside the thread or project files."}},"required":["name"],"additionalProperties":false}`

const searchSchema = `{"type":"object","properties":{"query":{"type":"string","maxLength":500,"description":"Text to find in file names or contents."}},"required":["query"],"additionalProperties":false}`

const writeSchema = `{"type":"object","properties":{"name":{"type":"string","maxLength":200,"description":"File name inside the thread files."},"content":{"type":"string","maxLength":100000,"description":"Full new text content."}},"required":["name","content"],"additionalProperties":false}`

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name: readName, Description: "Read a text file from this thread's or its project's files directory. Returns the text; refuses binary files, paths outside the files directory and files over the size cap.",
			InputSchema: json.RawMessage(readSchema), Handler: read, Effect: tool.Read,
			Flags: tool.Flags{PrivateData: true}, Annotated: true, Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: searchName, Description: "Search this thread's and its project's files for a text query, returning matching file names and line snippets.",
			InputSchema: json.RawMessage(searchSchema), Handler: search, Effect: tool.Read,
			Flags: tool.Flags{PrivateData: true}, Annotated: true, Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: writeName, Description: "Write a text file into this thread's files directory, replacing it if it exists. Undo restores the prior content or removes a new file.",
			InputSchema: json.RawMessage(writeSchema), Handler: write, Effect: tool.WriteUndo, Undo: UndoOp,
			Annotated: true, Contexts: tool.Kinds{tool.Chat},
		},
	}
}

func read(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	name, err := argName(args)
	if err != nil {
		return nil, err
	}
	path, err := resolve(ctx, tc, name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, tool.Error("no such file: " + name)
	}
	if err != nil {
		return nil, err
	}
	limits, err := filestore.ReadLimits(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if len(data) > limits.FileCap {
		return nil, tool.Error(fmt.Sprintf("%s is larger than the %d byte cap", name, limits.FileCap))
	}
	if !utf8.Valid(data) {
		return nil, tool.Error(name + " is not a text file")
	}
	return string(data), nil
}

func search(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("query")
	query, _ := raw.(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, tool.Error("query is required")
	}
	dirs, err := roots(ctx, tc)
	if err != nil {
		return nil, err
	}
	limits, err := filestore.ReadLimits(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !nameRe.MatchString(entry.Name()) {
				continue
			}
			if strings.Contains(entry.Name(), query) {
				out = append(out, entry.Name())
			}
			if len(out) >= 50 {
				return out, nil
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil || len(data) > limits.FileCap || !utf8.Valid(data) {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, query) {
					out = append(out, entry.Name()+": "+clip(strings.TrimSpace(line), 200))
					if len(out) >= 50 {
						return out, nil
					}
				}
			}
		}
	}
	if out == nil {
		return []string{}, nil
	}
	return out, nil
}

func write(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	name, err := argName(args)
	if err != nil {
		return nil, err
	}
	raw, _ := args.Get("content")
	content, _ := raw.(string)
	limits, err := filestore.ReadLimits(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if len(content) > limits.FileCap {
		return nil, tool.Error(fmt.Sprintf("content is larger than the %d byte cap", limits.FileCap))
	}
	dir, err := threadDir(tc)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	prior, priorErr := os.ReadFile(path)
	if priorErr != nil && !errors.Is(priorErr, os.ErrNotExist) {
		return nil, priorErr
	}
	var before any
	if priorErr == nil {
		if !utf8.Valid(prior) {
			return nil, tool.Error(name + " is not a text file")
		}
		before = string(prior)
	}
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		return nil, err
	}
	undo := map[string]any{"op": UndoOp, "path": path, "before": before, "sha": hash([]byte(content))}
	if _, err := writes.Record(ctx, tc.DB.Write, tc.Undoers, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: writeName,
		Target: name, Before: before, After: content, Undo: undo,
	}); err != nil {
		return nil, err
	}
	return "wrote " + name, nil
}

func RegisterUndoer(r *writes.Registry) {
	r.Register(UndoOp, func(_ context.Context, _ db.Querier, payload json.RawMessage) (any, error) {
		var p struct {
			Path   string  `json:"path"`
			Before *string `json:"before"`
			SHA    string  `json:"sha"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, err
		}
		if p.Path == "" {
			return nil, writes.UndoError("file_restore without a path")
		}
		current, err := os.ReadFile(p.Path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if hash(current) != p.SHA {
			return nil, writes.UndoError("file changed since the write; not restoring")
		}
		if p.Before == nil {
			if err := os.Remove(p.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			return "removed", nil
		}
		if err := os.WriteFile(p.Path, []byte(*p.Before), 0o666); err != nil {
			return nil, err
		}
		return "restored", nil
	})
}

func argName(args tool.Args) (string, error) {
	raw, _ := args.Get("name")
	name, _ := raw.(string)
	name = strings.TrimSpace(name)
	if !nameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", tool.Error("invalid file name; use a plain name inside the files directory")
	}
	return name, nil
}

func roots(ctx context.Context, tc *tool.Context) ([]string, error) {
	threadDir, err := threadDir(tc)
	if err != nil {
		return nil, err
	}
	out := []string{threadDir}
	thread, err := threads.Get(ctx, tc.DB.Read, tc.ThreadID)
	if err != nil {
		return nil, err
	}
	if thread != nil && thread.ProjectID != nil && *thread.ProjectID != 0 {
		out = append(out, filepath.Join(tc.Cfg.FilesDir(), filestore.ProjectOwner(*thread.ProjectID).Dir()))
	}
	return out, nil
}

func resolve(ctx context.Context, tc *tool.Context, name string) (string, error) {
	dirs, err := roots(ctx, tc)
	if err != nil {
		return "", err
	}
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return filepath.Join(dirs[0], name), nil
}

func threadDir(tc *tool.Context) (string, error) {
	return filepath.Join(tc.Cfg.FilesDir(), filestore.ThreadOwner(tc.ThreadID).Dir()), nil
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func clip(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max] + "…"
}
