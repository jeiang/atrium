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
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /chat", Uses: uses(asstapp.HomeName)},
			{Kind: application.HTML, Target: "POST /chat", Uses: uses(asstapp.ThreadStartName)},
			{Kind: application.HTML, Target: "POST /chat/{thread_id}/messages", Uses: uses(asstapp.MessageSendName, asstapp.MessageEditName, asstapp.ThreadTranscriptName)},
			{Kind: application.API, Target: "POST /threads", Uses: uses(asstapp.ThreadStartName)},
			{Kind: application.API, Target: "POST /threads/{thread_id}/messages", Uses: uses(asstapp.MessageSendName, asstapp.MessageEditName)},
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
