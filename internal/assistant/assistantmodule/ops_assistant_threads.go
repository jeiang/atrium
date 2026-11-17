package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/unit"
)

func threadOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeThreadList(), asstapp.DescribeThreadTranscript(), asstapp.DescribeThreadWork(), asstapp.DescribeThreadHistory(),
			asstapp.DescribeRunEvents(), asstapp.DescribeRunDetail(), asstapp.DescribeHome(), asstapp.DescribeAbout(), asstapp.DescribeTodayEnsure(),
			asstapp.DescribeThreadStart(), asstapp.DescribeMessageSend(), asstapp.DescribeMessageEdit(),
			asstapp.DescribeReplyRegenerate(), asstapp.DescribeThreadBranch(), asstapp.DescribeThreadDelete(), asstapp.DescribeThreadPin(),
			asstapp.DescribeThreadArchive(), asstapp.DescribeThreadProject(), asstapp.DescribeThreadOptions(),
			asstapp.DescribeRunStop(), asstapp.DescribeRunResume(), asstapp.DescribeRunAnswer(), asstapp.DescribeStreamRetention(),
		}, append(apiBindings(nil), []application.Binding{
			{Kind: application.CLI, Target: "agent run", Uses: uses(asstapp.ThreadStartName, asstapp.MessageSendName)},
			{Kind: application.Job, Target: unit.Assistant + "/" + assistant.StreamRetentionName, Uses: uses(asstapp.StreamRetentionName)},
		}...)
}
