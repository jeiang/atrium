package notify

import "github.com/jeiang/atrium/internal/connections"

func ImportV2(values map[string]string, vapid connections.ImportCredential) connections.ImportService {
	source := connections.SourceOperator
	if values["webpush_ca_file"] != "" {
		source = connections.SourceHost
	}
	return connections.ImportService{
		ID:           string(KindWebPush),
		Kind:         string(KindWebPush),
		Label:        "Web push",
		Source:       source,
		Config:       map[string]string{"subject": values["webpush_subject"]},
		Transport:    map[string]string{"ca_file": values["webpush_ca_file"]},
		Capabilities: []string{"delivery"},
		Credentials:  []connections.ImportCredential{vapid},
	}
}
