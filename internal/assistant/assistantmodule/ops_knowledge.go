package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

func knowledgeService(d *web.Deps) *kbapp.Service { return kbapp.New(d.DB, assistant.Now) }

func knowledgeToolService(tc *tool.Context) *kbapp.Service { return kbapp.New(tc.DB, tc.Now) }

func knowledgeOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			kbapp.DescribeList(), kbapp.DescribeDetail(), kbapp.DescribeProfile(), kbapp.DescribeSearch(), kbapp.DescribeGet(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /library/knowledge", Uses: []application.Name{kbapp.ListName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/{record_id}", Uses: []application.Name{kbapp.DetailName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/{record_id}/purge", Uses: []application.Name{kbapp.DetailName}},
			{Kind: application.HTML, Target: "GET /library/knowledge/profile", Uses: []application.Name{kbapp.ProfileName}},
			{Kind: application.Tool, Target: "kb_search", Uses: []application.Name{kbapp.SearchName}},
			{Kind: application.Tool, Target: "kb_get", Uses: []application.Name{kbapp.GetName}},
		}
}
