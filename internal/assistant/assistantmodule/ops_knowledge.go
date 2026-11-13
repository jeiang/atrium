package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/tool"
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

func knowledgeOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			kbapp.DescribeList(), kbapp.DescribeDetail(), kbapp.DescribeProfile(), kbapp.DescribeSearch(), kbapp.DescribeGet(),
			kbapp.DescribeCreate(), kbapp.DescribeEdit(), kbapp.DescribeConfirm(), kbapp.DescribeRetract(), kbapp.DescribeRestore(),
			kbapp.DescribePurge(), kbapp.DescribeRetractRun(), kbapp.DescribeConfirmSelected(), kbapp.DescribeConfirmMatching(),
			kbapp.DescribeAgentWrite(), kbapp.DescribeAgentRetract(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /library/knowledge", Uses: []application.Name{kbapp.ListName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/{record_id}", Uses: []application.Name{kbapp.DetailName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/{record_id}/purge", Uses: []application.Name{kbapp.DetailName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/profile", Uses: []application.Name{kbapp.ProfileName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/new", Uses: []application.Name{kbapp.CreateName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/new", Uses: []application.Name{kbapp.CreateName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/profile", Uses: []application.Name{kbapp.CreateName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/{record_id}", Uses: []application.Name{kbapp.EditName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/confirm", Uses: []application.Name{kbapp.ConfirmSelectedName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/confirm-all", Uses: []application.Name{kbapp.ConfirmMatchingName, kbapp.ListName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/{record_id}/confirm", Uses: []application.Name{kbapp.ConfirmName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/{record_id}/retract", Uses: []application.Name{kbapp.RetractName, kbapp.DetailName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/{record_id}/restore", Uses: []application.Name{kbapp.RestoreName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/{record_id}/purge", Uses: []application.Name{kbapp.PurgeName}},
			{Kind: application.HTML, Target: "POST /library/knowledge/runs/{run_id}/retract", Uses: []application.Name{kbapp.RetractRunName}},
			{Kind: application.Tool, Target: "kb_search", Uses: []application.Name{kbapp.SearchName}},
			{Kind: application.Tool, Target: "kb_get", Uses: []application.Name{kbapp.GetName}},
			{Kind: application.Tool, Target: "kb_write", Uses: []application.Name{kbapp.AgentWriteName}},
			{Kind: application.Tool, Target: "kb_retract", Uses: []application.Name{kbapp.AgentRetractName}},
		}
}
