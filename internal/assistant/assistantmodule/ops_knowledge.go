package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/kb/kbcmd"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
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

func knowledgeCLIService(env config.Env) kbcmd.Open {
	return func(ctx context.Context) (*kbapp.Service, func(), error) {
		cfg, err := config.Load(env)
		if err != nil {
			return nil, nil, err
		}
		key, err := dbkey.Load(env, settings.From(ctx))
		if err != nil {
			return nil, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(key))
		if err != nil {
			return nil, nil, err
		}
		return kbapp.New(d, nil, assistant.Now), func() { _ = d.Close() }, nil
	}
}

func knowledgeToolService(tc *tool.Context) *kbapp.Service {
	return kbapp.New(tc.DB, tc.Ledger, tc.Now)
}

func (m Module) knowledgeOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			kbapp.DescribeList(), kbapp.DescribeDetail(), kbapp.DescribeProfile(), kbapp.DescribeSearch(), kbapp.DescribeGet(),
			kbapp.DescribeCreate(), kbapp.DescribeEdit(), kbapp.DescribeConfirm(), kbapp.DescribeRetract(), kbapp.DescribeRestore(),
			kbapp.DescribePurge(), kbapp.DescribeRetractRun(), kbapp.DescribeConfirmSelected(), kbapp.DescribeConfirmMatching(),
			kbapp.DescribeAgentWrite(), kbapp.DescribeAgentRetract(), kbapp.DescribeImport(), kbapp.DescribeExport(),
		}, append(m.knowledgeRoutes(nil), []application.Binding{
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
			{Kind: application.CLI, Target: "kb import", Uses: []application.Name{kbapp.ImportName}},
			{Kind: application.Job, Target: module.Assistant + "/" + assistant.KnowledgeExportName, Uses: []application.Name{kbapp.ExportName}},
			{Kind: application.Tool, Target: "kb_write", Uses: []application.Name{kbapp.AgentWriteName}},
			{Kind: application.Tool, Target: "kb_retract", Uses: []application.Name{kbapp.AgentRetractName}},
		}...)
}
