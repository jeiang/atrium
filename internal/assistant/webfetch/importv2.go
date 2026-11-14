package webfetch

import (
	"github.com/jeiang/atrium/internal/connections"
)

const DefaultProbeURL = "https://example.com/"

func ImportV2() connections.ImportService {
	return connections.ImportService{
		ID:           string(KindWebfetch),
		Kind:         string(KindWebfetch),
		Label:        "Web fetch",
		Config:       map[string]string{"url": DefaultProbeURL},
		Capabilities: []string{"egress"},
	}
}
