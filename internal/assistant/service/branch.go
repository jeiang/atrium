package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

func BranchMessages(messages []journal.Message, seq int64) ([]journal.Message, error) {
	var kept []journal.Message
	var following *journal.Message
	for i, m := range messages {
		if m.Seq <= seq {
			kept = append(kept, m)
		} else if following == nil {
			following = &messages[i]
		}
	}
	n := len(kept)
	if n == 0 || kept[n-1].Role != journal.Assistant {
		return kept, nil
	}
	hasUse, err := block.HasKind(kept[n-1].Content, block.ToolUse)
	if err != nil {
		return nil, err
	}
	if !hasUse {
		return kept, nil
	}
	if following != nil && following.Role == journal.User {
		hasResult, err := block.HasKind(following.Content, block.ToolResult)
		if err != nil {
			return nil, err
		}
		if hasResult {
			return append(kept, *following), nil
		}
	}
	last := kept[n-1]
	blocks, err := block.DecodeAll(last.Content)
	if err != nil {
		return nil, err
	}
	var content []json.RawMessage
	for _, b := range blocks {
		if b.Kind != block.ToolUse {
			content = append(content, b.Raw)
		}
	}
	last.Content = content
	if len(last.Content) == 0 {
		return kept[:n-1], nil
	}
	kept[n-1] = last
	return kept, nil
}

func (s *Service) Branch(ctx context.Context, threadID, seq int64) (int64, error) {
	source, err := threads.Get(ctx, s.DB.Read, threadID)
	if err != nil {
		return 0, err
	}
	if source == nil {
		return 0, UnknownThread(threadID)
	}
	history, err := journal.LoadHistory(ctx, s.DB.Read, threadID)
	if err != nil {
		return 0, err
	}
	all := history.Messages
	if !slices.ContainsFunc(all, func(m journal.Message) bool { return m.Seq == seq }) {
		return 0, NothingToRedo(fmt.Sprintf("no message %d to branch from", seq))
	}
	messages, err := BranchMessages(all, seq)
	if err != nil {
		return 0, err
	}
	marks, err := threads.ReadMarks(ctx, s.DB.Read, threadID)
	if err != nil {
		return 0, err
	}
	var limit any
	if source.ContextLimit != nil {
		limit = *source.ContextLimit
	}
	child, err := threads.Create(ctx, s.DB.Write, s.now(), threads.Spec{
		Kind: threads.Chat, Title: source.Title, ProjectID: source.ProjectID,
		Model: source.Model, Effort: source.Effort, ContextLimit: limit,
		AllowedURLs: source.AllowedURLs, BranchedFromThreadID: &threadID, BranchedFromSeq: &seq,
		Marks: marks,
	})
	if err != nil {
		return 0, err
	}
	for _, m := range messages {
		content, err := files.CloneContent(ctx, s.DB.Write, files.ScopeOf(threadID, source.ProjectID), m.Content, child, s.now())
		if err != nil {
			return 0, err
		}
		if _, err := journal.AppendMessage(ctx, s.DB, child, nil, m.Role, content, s.now(), m.SystemSig); err != nil {
			return 0, err
		}
	}
	if err := memory.Rebuild(ctx, s.DB.Write, child, s.now()); err != nil {
		return 0, err
	}
	return child, nil
}
