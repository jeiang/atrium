package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/davmirror"
)

const KindICloud connections.Kind = "icloud"

const (
	CapabilityCalDAV  = "caldav"
	CapabilityCardDAV = "carddav"
)

func init() {
	for _, d := range Connections() {
		connections.Register(d)
	}
}

func Connections() []connections.Definition {
	return []connections.Definition{{Kind: KindICloud, Validate: validateICloud, Test: testICloud}}
}

func validateICloud(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("icloud config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "apple_id":
		case "caldav_url", "carddav_url":
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				return fmt.Errorf("icloud %s must be an http or https URL", key)
			}
		default:
			return fmt.Errorf("icloud config has no %s field", key)
		}
	}
	for _, cap := range c.Capabilities {
		if cap.Name != CapabilityCalDAV && cap.Name != CapabilityCardDAV {
			return fmt.Errorf("icloud has no %s capability", cap.Name)
		}
	}
	return nil
}

func icloudPassword(ctx context.Context, lease *connections.Lease, account string) (string, error) {
	password, err := lease.Credential("mail/" + account + "/password")
	if err == nil && password != "" {
		return password, nil
	}
	password, _ = config.From(ctx).Password(account)
	if password == "" {
		return "", errors.New("the iCloud app password is not set")
	}
	return password, nil
}

func testICloud(ctx context.Context, lease *connections.Lease, caps []connections.Capability) ([]connections.Result, error) {
	account := strings.TrimPrefix(string(lease.ID()), "icloud/")
	spec, ok := config.From(ctx).Account(account)
	if !ok || spec.Preset != config.ICloud {
		return []connections.Result{{Capability: CapabilityCalDAV, State: connections.Unconfigured, Reason: "no enabled iCloud mail account"}}, nil
	}
	password, err := icloudPassword(ctx, lease, account)
	if err != nil {
		return []connections.Result{{Capability: CapabilityCalDAV, State: connections.NeedsReauth, Reason: err.Error()}}, nil
	}
	var cfg map[string]string
	_ = json.Unmarshal(lease.Config(), &cfg)
	apple := strings.TrimSpace(cfg["apple_id"])
	if apple == "" {
		apple = spec.Login
	}
	base, err := lease.Transport()
	if err != nil {
		return nil, err
	}
	httpClient := davmirror.BasicClient(base, apple, password)
	endpoints := EndpointsFrom(config.OS)
	results := make([]connections.Result, 0, len(caps))
	for _, cap := range caps {
		if !cap.Enabled {
			continue
		}
		var kind davmirror.Kind
		var endpoint string
		switch cap.Name {
		case CapabilityCalDAV:
			kind, endpoint = davmirror.Calendars, endpoints.CalDAV
		case CapabilityCardDAV:
			kind, endpoint = davmirror.Contacts, endpoints.CardDAV
		default:
			results = append(results, connections.Result{Capability: cap.Name, State: connections.Failing, Reason: "unknown iCloud capability"})
			continue
		}
		client, err := davmirror.New(httpClient, endpoint)
		if err != nil {
			results = append(results, connections.Result{Capability: cap.Name, State: connections.Failing, Reason: err.Error()})
			continue
		}
		collections, err := client.Collections(ctx, kind)
		var status *davmirror.StatusError
		switch {
		case errors.As(err, &status) && (status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden):
			results = append(results, connections.Result{Capability: cap.Name, State: connections.NeedsReauth, Reason: "iCloud refused the app password"})
		case err != nil:
			results = append(results, connections.Result{Capability: cap.Name, State: connections.Failing, Reason: err.Error()})
		default:
			data, _ := json.Marshal(map[string]any{"collections": len(collections)})
			results = append(results, connections.Result{Capability: cap.Name, State: connections.OK,
				Reason: fmt.Sprintf("%d collections", len(collections)), Data: data})
		}
	}
	if len(results) == 0 {
		return []connections.Result{{Capability: CapabilityCalDAV, State: connections.Unconfigured, Reason: "no iCloud capability is enabled"}}, nil
	}
	return results, nil
}
