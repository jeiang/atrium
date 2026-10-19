package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/store"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/py"
)

// BranchMessages is branch_messages: the history up to and including seq. A tool_use turn at the cut takes
// its results along, or loses its tool_use blocks when no results exist yet, so the copy never ends on an
// unanswered call.
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

// Branch is branch: a new chat thread whose history is this thread's conversation up to and including message
// seq, with its options, allowlist, project and attachments (copied, so either thread can be deleted).
func (s *Service) Branch(ctx context.Context, threadID, seq int64) (int64, error) {
	source, err := threads.Get(ctx, s.DB.Read, threadID)
	if err != nil {
		return 0, err
	}
	if source == nil {
		return 0, UnknownThread(threadID)
	}
	all, err := journal.MessagesOf(ctx, s.DB.Read, threadID, false)
	if err != nil {
		return 0, err
	}
	if !slices.ContainsFunc(all, func(m journal.Message) bool { return m.Seq == seq }) {
		return 0, NothingToRedo(fmt.Sprintf("no message %d to branch from", seq))
	}
	messages := BranchMessages(all, seq)
	child, err := CreateThread(ctx, s.DB.Write, s.now(), threads.NewThread{Kind: threads.Chat, Title: source.Title})
	if err != nil {
		return 0, err
	}
	urls := source.AllowedURLs
	if urls == nil {
		urls = []string{}
	}
	if _, err := s.DB.Write.ExecContext(ctx,
		"UPDATE threads SET effort = ?, context_limit = ?, allowed_urls = ?, project_id = ?, code_sandbox = ?, branched_from_thread_id = ?, branched_from_seq = ? WHERE id = ?",
		source.Effort, source.ContextLimit, py.JSONDumps(urls), source.ProjectID, store.Bit(bool(source.CodeSandbox)), threadID, seq, child); err != nil {
		return 0, err
	}
	for _, m := range messages {
		content, err := files.CloneContent(ctx, s.DB.Write, m.Content, child, s.now())
		if err != nil {
			return 0, err
		}
		if _, err := journal.AppendMessage(ctx, s.DB, child, nil, m.Role, content, s.now(), m.SystemSig); err != nil {
			return 0, err
		}
	}
	return child, nil
}
