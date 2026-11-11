package tracking

import (
	"github.com/jeiang/atrium/internal/connections"
)

func ImportV2(values map[string]string, key connections.ImportCredential, health *connections.ImportHealth) connections.ImportService {
	return connections.ImportService{
		ID:           string(ConnectionID),
		Kind:         string(KindShip24),
		Label:        "Ship24",
		Source:       connections.SourceHost,
		Config:       map[string]string{"url": values["mail_ship24_url"]},
		Transport:    map[string]string{"ca_file": values["ship24_ca_file"], "proxy": values["ship24_proxy"]},
		Capabilities: []string{"api"},
		Credentials:  []connections.ImportCredential{key},
		Health:       health,
	}
}
