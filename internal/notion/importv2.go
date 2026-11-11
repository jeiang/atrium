package notion

import (
	"github.com/jeiang/atrium/internal/connections"
)

func ImportV2(values map[string]string, token connections.ImportCredential) connections.ImportService {
	return connections.ImportService{
		ID:           string(KindNotion),
		Kind:         string(KindNotion),
		Label:        "Notion",
		Config:       map[string]string{"api": BaseURL},
		Transport:    map[string]string{"ca_file": values["notion_ca_file"], "proxy": values["notion_proxy"]},
		Capabilities: []string{"api"},
		Credentials:  []connections.ImportCredential{token},
	}
}
