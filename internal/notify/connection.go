package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/jeiang/atrium/internal/connections"
)

func init() {
	for _, d := range Connections() {
		connections.Register(d)
	}
}

func Connections() []connections.Definition {
	return []connections.Definition{{
		Kind:     KindWebPush,
		Validate: validatePushConnection,
		Test:     testPushConnection,
		Silent:   silentPushConnection,
	}}
}

func silentPushConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	subject := strings.TrimSpace(connectionConfig(lease)["subject"])
	if subject == "" {
		return []connections.Result{{Capability: "delivery", State: connections.Unconfigured, Reason: "no subject is set"}}, nil
	}
	if err := subjectCheck(subject); err != nil {
		return []connections.Result{{Capability: "delivery", State: connections.Failing, Reason: err.Error()}}, nil
	}
	if _, err := vapidKeys(lease); err != nil {
		return []connections.Result{{Capability: "delivery", State: connections.Failing, Reason: err.Error()}}, nil
	}
	d := lease.DB()
	if d == nil {
		return nil, errors.New("web push health needs a writable database")
	}
	var devices int
	if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM push_subscriptions").Scan(&devices); err != nil {
		return nil, err
	}
	if devices == 0 {
		return []connections.Result{{Capability: "delivery", State: connections.Unconfigured, Reason: "no push subscriptions"}}, nil
	}
	return []connections.Result{{Capability: "delivery", State: connections.OK, Reason: "the VAPID key and subject are valid"}}, nil
}

func validatePushConnection(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("web push config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "subject":
			if err := subjectCheck(value); err != nil {
				return fmt.Errorf("web push subject %w", err)
			}
		default:
			return fmt.Errorf("web push config has no %s field", key)
		}
	}
	return nil
}

func testPushConnection(ctx context.Context, lease *connections.Lease, _ []connections.Capability) ([]connections.Result, error) {
	subject := strings.TrimSpace(connectionConfig(lease)["subject"])
	if subject == "" {
		return []connections.Result{{Capability: "delivery", State: connections.Unconfigured, Reason: "no subject is set"}}, nil
	}
	if err := subjectCheck(subject); err != nil {
		return []connections.Result{{Capability: "delivery", State: connections.Failing, Reason: err.Error()}}, nil
	}
	if _, err := vapidKeys(lease); err != nil {
		return []connections.Result{{Capability: "delivery", State: connections.Failing, Reason: err.Error()}}, nil
	}
	d := lease.DB()
	if d == nil {
		return nil, errors.New("web push test needs a writable database")
	}
	var devices int
	if err := d.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM push_subscriptions").Scan(&devices); err != nil {
		return nil, err
	}
	if devices == 0 {
		return []connections.Result{{Capability: "delivery", State: connections.Unconfigured, Reason: "no push subscriptions"}}, nil
	}
	payload, err := json.Marshal(pushPayload{Title: "atrium test", Body: "Test push notification.", URL: pushInbox})
	if err != nil {
		return nil, err
	}
	sent, failure, err := pushAll(ctx, d, System, payload, webpush.UrgencyNormal)
	if err != nil {
		return nil, err
	}
	if sent == 0 {
		if failure == "" {
			failure = "no device accepted the diagnostic push"
		}
		return []connections.Result{{Capability: "delivery", State: connections.Failing, Reason: failure}}, nil
	}
	return []connections.Result{{Capability: "delivery", State: connections.OK, Reason: fmt.Sprintf("delivered to %d device(s)", sent)}}, nil
}
