package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

func (m Module) knowledgeService(d *web.Deps) *kbapp.Service {
	svc, err := m.Jobs.Service(d.Ctx)
	if err != nil {
		panic(err)
	}
	return kbapp.New(d.DB, svc.Runner.Ledger, assistant.Now)
}

func knowledgeToolService(tc *tool.Context) *kbapp.Service {
	return kbapp.New(tc.DB, tc.Ledger, tc.Now)
}

func (m Module) knowledgeOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			kbapp.DescribeList(), kbapp.DescribeDetail(), kbapp.DescribeInstructions(), kbapp.DescribeProfile(), kbapp.DescribeSearch(), kbapp.DescribeGet(),
			kbapp.DescribeCreate(), kbapp.DescribeEdit(), kbapp.DescribeConfirm(), kbapp.DescribeRetract(), kbapp.DescribeRestore(),
			kbapp.DescribePurge(), kbapp.DescribeRetractRun(), kbapp.DescribeConfirmSelected(), kbapp.DescribeConfirmMatching(),
			kbapp.DescribeAgentWrite(), kbapp.DescribeAgentRetract(), kbapp.DescribeExport(),
			kbapp.DescribeSubjects(), kbapp.DescribeSubject(), kbapp.DescribeMap(),
			kbapp.DescribeLinks(), kbapp.DescribeLink(), kbapp.DescribeUnlink(),
		}, append(m.knowledgeRoutes(nil), []application.Binding{
			{Kind: application.Tool, Target: "kb_search", Uses: []application.Name{kbapp.SearchName}},
			{Kind: application.Tool, Target: "kb_get", Uses: []application.Name{kbapp.GetName}},
			{Kind: application.Job, Target: unit.Assistant + "/" + assistant.KnowledgeExportName, Uses: []application.Name{kbapp.ExportName}},
			{Kind: application.Tool, Target: "kb_write", Uses: []application.Name{kbapp.AgentWriteName}},
			{Kind: application.Tool, Target: "kb_retract", Uses: []application.Name{kbapp.AgentRetractName}},
		}...)
}
