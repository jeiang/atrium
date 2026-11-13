package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
)

func threadOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeThreadList(), asstapp.DescribeThreadTranscript(), asstapp.DescribeThreadWork(), asstapp.DescribeThreadHistory(),
			asstapp.DescribeRunEvents(), asstapp.DescribeRunDetail(), asstapp.DescribeHome(), asstapp.DescribeAbout(), asstapp.DescribeTodayEnsure(),
			asstapp.DescribeThreadStart(), asstapp.DescribeMessageSend(), asstapp.DescribeMessageEdit(),
			asstapp.DescribeReplyRegenerate(), asstapp.DescribeThreadBranch(), asstapp.DescribeThreadDelete(), asstapp.DescribeThreadPin(),
			asstapp.DescribeThreadArchive(), asstapp.DescribeThreadProject(), asstapp.DescribeThreadOptions(),
			asstapp.DescribeRunStop(), asstapp.DescribeRunResume(), asstapp.DescribeRunAnswer(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /chat", Uses: uses(asstapp.HomeName)},
			{Kind: application.HTML, Target: "POST /chat", Uses: uses(asstapp.ThreadStartName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/messages", Uses: uses(asstapp.MessageSendName, asstapp.MessageEditName, asstapp.ThreadTranscriptName)},
			{Kind: application.API, Target: "POST /threads", Uses: uses(asstapp.ThreadStartName)},
			{Kind: application.API, Target: "POST /threads/{thread_id}/messages", Uses: uses(asstapp.MessageSendName, asstapp.MessageEditName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/pin", Uses: uses(asstapp.ThreadPinName, asstapp.HomeName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/archive", Uses: uses(asstapp.ThreadArchiveName, asstapp.HomeName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/project", Uses: uses(asstapp.ThreadProjectName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/options", Uses: uses(asstapp.ThreadOptionsName, asstapp.ThreadTranscriptName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/delete", Uses: uses(asstapp.ThreadDeleteName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/regenerate", Uses: uses(asstapp.ReplyRegenerateName, asstapp.ThreadTranscriptName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/stop", Uses: uses(asstapp.RunStopName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/branch", Uses: uses(asstapp.ThreadBranchName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/runs/{run_id}/resume", Uses: uses(asstapp.RunResumeName, asstapp.RunDetailName, asstapp.ThreadTranscriptName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/answer", Uses: uses(asstapp.RunAnswerName, asstapp.ThreadTranscriptName)},
			{Kind: application.API, Target: "POST /threads/{thread_id}/stop", Uses: uses(asstapp.RunStopName)},
			{Kind: application.API, Target: "POST /threads/{thread_id}/answer", Uses: uses(asstapp.RunAnswerName)},
			{Kind: application.CLI, Target: "agent run", Uses: uses(asstapp.ThreadStartName, asstapp.MessageSendName)},
			{Kind: application.HTML, Target: "GET /chat/palette", Uses: uses(asstapp.ThreadListName)},
			{Kind: application.HTML, Target: "GET /today", Uses: uses(asstapp.TodayEnsureName)},
			{Kind: application.HTML, Target: "GET /chat/agenda", Uses: uses(asstapp.HomeName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}", Uses: uses(asstapp.ThreadTranscriptName, asstapp.ThreadWorkName, asstapp.ProjectListName, asstapp.ProjectDetailName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/work", Uses: uses(asstapp.ThreadTranscriptName, asstapp.ThreadWorkName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/history", Uses: uses(asstapp.ThreadHistoryName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/history/{start}/{n}", Uses: uses(asstapp.ThreadHistoryName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/events", Uses: uses(asstapp.RunEventsName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/runs/{run_id}", Uses: uses(asstapp.RunDetailName, asstapp.ThreadTranscriptName)},
			{Kind: application.API, Target: "GET /threads", Uses: uses(asstapp.ThreadListName)},
			{Kind: application.API, Target: "GET /threads/{thread_id}", Uses: uses(asstapp.ThreadTranscriptName)},
			{Kind: application.API, Target: "GET /threads/{thread_id}/runs/{run_id}/events", Uses: uses(asstapp.RunEventsName)},
		}
}
