package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/threads"
)

func BranchMessages(messages []journal.Message, seq int64) []journal.Message {
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
	if n == 0 || kept[n-1].Role != journal.Assistant || !hasBlock(kept[n-1].Content, actx.ToolUse) {
		return kept
	}
	if following != nil && following.Role == journal.User && hasBlock(following.Content, actx.ToolResult) {
		return append(kept, *following)
	}
	last := kept[n-1]
	last.Content = slices.DeleteFunc(slices.Clone(last.Content), func(b json.RawMessage) bool { return blockKind(b) == actx.ToolUse })
	if len(last.Content) == 0 {
		return kept[:n-1]
	}
	kept[n-1] = last
	return kept
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
	messages := BranchMessages(all, seq)
	var limit any
	if source.ContextLimit != nil {
		limit = *source.ContextLimit
	}
	child, err := threads.Create(ctx, s.DB.Write, s.now(), threads.Spec{
		Kind: threads.Chat, Title: source.Title, ProjectID: source.ProjectID,
		Model: source.Model, Effort: source.Effort, ContextLimit: limit,
		AllowedURLs: source.AllowedURLs, BranchedFromThreadID: &threadID, BranchedFromSeq: &seq,
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
	return child, nil
}
