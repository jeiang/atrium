package grafanatools

import (
	"github.com/jeiang/atrium/internal/connections"
)

func ImportV2(values map[string]string, token connections.ImportCredential) connections.ImportService {
	return connections.ImportService{
		ID:    string(KindGrafana),
		Kind:  string(KindGrafana),
		Label: "Grafana",
		Config: map[string]string{
			"url":         values[KeyURL],
			"metrics_uid": values[KeyMetricsUID],
			"logs_uid":    values[KeyLogsUID],
			"logs_type":   values[KeyLogsType],
			"logs_query":  values[KeyLogsQuery],
		},
		Transport:    map[string]string{"ca_file": values["grafana_ca_file"], "proxy": values["grafana_proxy"]},
		Capabilities: []string{"viewer"},
		Credentials:  []connections.ImportCredential{token},
	}
}
