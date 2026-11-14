package filetools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	readName   = "file_read"
	searchName = "file_search"
	writeName  = "file_write"
)

const readSchema = `{"type":"object","properties":{"name":{"type":"string","maxLength":200,"description":"File name inside the thread or project files."}},"required":["name"],"additionalProperties":false}`

const searchSchema = `{"type":"object","properties":{"query":{"type":"string","maxLength":500,"description":"Text to find in file names or contents."}},"required":["query"],"additionalProperties":false}`

const writeSchema = `{"type":"object","properties":{"name":{"type":"string","maxLength":200,"description":"File name inside the thread files."},"content":{"type":"string","maxLength":100000,"description":"Full new text content."}},"required":["name","content"],"additionalProperties":false}`

type Service func(*tool.Context) *asstapp.Service

func Tools(svc Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: readName, Description: "Read a text file from this thread's or its project's files directory. Returns the text; refuses binary files, paths outside the files directory and files over the size cap.",
			InputSchema: json.RawMessage(readSchema), Handler: read(svc), Effect: tool.Read,
			Marks: tool.Messages | tool.Untrusted, Annotated: true, Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: searchName, Description: "Search this thread's and its project's files for a text query, returning matching file names and line snippets.",
			InputSchema: json.RawMessage(searchSchema), Handler: search(svc), Effect: tool.Read,
			Marks: tool.Messages | tool.Untrusted, Annotated: true, Contexts: tool.Kinds{tool.Chat, tool.Cron, tool.Nightly, tool.Sub},
		},
		{
			Name: writeName, Description: "Write a text file into this thread's files directory, replacing it if it exists. Undo restores the prior content or removes a new file.",
			InputSchema: json.RawMessage(writeSchema), Handler: write(svc), Effect: tool.WriteUndo, Undo: asstapp.FileRestoreOp,
			Annotated: true, Channel: tool.ChannelOwn, Contexts: tool.Kinds{tool.Chat},
		},
	}
}

func fail(err error) error {
	var e *application.Error
	if errors.As(err, &e) {
		return tool.Fail(err)
	}
	return err
}

func text(args tool.Args, name string) string {
	raw, _ := args.Get(name)
	value, _ := raw.(string)
	return value
}

func read(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		got, err := svc(tc).FileRead(ctx, asstapp.FileReadInput{ThreadID: tc.ThreadID, Name: text(args, "name")})
		if err != nil {
			return nil, fail(err)
		}
		return tool.Marked{Value: got.Data.Text, Marks: tool.Messages | tool.Untrusted}, nil
	}
}

func search(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		got, err := svc(tc).FileSearch(ctx, asstapp.FileSearchInput{ThreadID: tc.ThreadID, Query: text(args, "query")})
		if err != nil {
			return nil, fail(err)
		}
		if len(got.Data) == 0 {
			return tool.Marked{Value: []string{}, Marks: 0}, nil
		}
		return tool.Marked{Value: got.Data, Marks: tool.Messages | tool.Untrusted}, nil
	}
}

func write(svc Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		name := text(args, "name")
		_, err := svc(tc).FileWrite(ctx, asstapp.FileWriteInput{ThreadID: tc.ThreadID, RunID: tc.RunID, ProposalID: tc.ProposalID,
			Name: name, Content: text(args, "content")})
		if err != nil {
			return nil, fail(err)
		}
		return "wrote " + strings.TrimSpace(name), nil
	}
}
