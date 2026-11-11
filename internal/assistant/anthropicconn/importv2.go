package anthropicconn

import (
	"github.com/jeiang/atrium/internal/connections"
)

const DefaultURL = "https://api.anthropic.com"

func ImportV2(values map[string]string, key connections.ImportCredential) connections.ImportService {
	return connections.ImportService{
		ID:           string(Kind),
		Kind:         string(Kind),
		Label:        "Anthropic",
		Config:       map[string]string{"url": DefaultURL},
		Transport:    map[string]string{"ca_file": values["anthropic_ca_file"], "proxy": values["anthropic_proxy"]},
		Capabilities: []string{"models"},
		Credentials:  []connections.ImportCredential{key},
	}
}
