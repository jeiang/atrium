package mailmodule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/mail/gmailauth"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/mailconn"
	"github.com/jeiang/atrium/internal/mail/smtp"
)

func init() {
	for _, d := range Connections() {
		connections.Register(d)
	}
}

func Connections() []connections.Definition {
	return []connections.Definition{{Kind: mailconn.Kind, Validate: validateConnection, Test: testConnection}}
}

func validateConnection(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("mail config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "preset":
			if _, err := config.NewAccount("probe", "probe", value, "probe", true); err != nil {
				return fmt.Errorf("mail preset: %w", err)
			}
		case "login", "imap_host", "smtp_host":
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("mail %s must not be empty", key)
			}
		case "auth_method":
			if _, err := config.ParseAuthMethod(value); err != nil {
				return err
			}
		case "enabled":
			if value != "true" && value != "false" {
				return fmt.Errorf("mail enabled must be true or false")
			}
		default:
			return fmt.Errorf("mail config has no %s field", key)
		}
	}
	for _, cap := range c.Capabilities {
		if cap.Name != mailconn.CapabilityIMAP && cap.Name != mailconn.CapabilitySMTP {
			return fmt.Errorf("mail has no %s capability", cap.Name)
		}
	}
	return nil
}

func testConnection(ctx context.Context, lease *connections.Lease, caps []connections.Capability) ([]connections.Result, error) {
	account := mailconn.Account(lease.ID())
	spec, err := candidateSpec(account, mailconn.Settings(lease))
	if err != nil {
		return []connections.Result{{Capability: mailconn.CapabilityIMAP, State: connections.Failing, Reason: err.Error()}}, nil
	}
	if !spec.Enabled {
		return []connections.Result{{Capability: mailconn.CapabilityIMAP, State: connections.Unconfigured, Reason: "no enabled mail account " + account}}, nil
	}
	method := spec.AuthMethod
	var source *gmailauth.Source
	if method == config.AuthXOAUTH2 {
		resolved, shared, err := gmailauth.Resolve(ctx, lease.DB().Read, account)
		if err != nil {
			return []connections.Result{{Capability: mailconn.CapabilityIMAP, State: connections.NeedsReauth, Reason: err.Error()}}, nil
		}
		if resolved != config.AuthXOAUTH2 || shared == nil {
			return []connections.Result{{Capability: mailconn.CapabilityIMAP, State: connections.NeedsReauth, Reason: "the account has no XOAUTH2 grant"}}, nil
		}
		method, source = resolved, shared
		defer source.Close()
	}
	var password string
	if method != config.AuthXOAUTH2 {
		value, err := mailconn.Password(lease, account)
		if err != nil {
			return []connections.Result{{Capability: mailconn.CapabilityIMAP, State: connections.NeedsReauth, Reason: "the app password is not set"}}, nil
		}
		password = value
	}
	var results []connections.Result
	for _, cap := range caps {
		if !cap.Enabled {
			continue
		}
		switch cap.Name {
		case mailconn.CapabilityIMAP:
			results = append(results, testIMAP(ctx, lease, spec, method, password, source))
		case mailconn.CapabilitySMTP:
			results = append(results, testSMTP(ctx, lease, spec, method, password, source))
		default:
			results = append(results, connections.Result{Capability: cap.Name, State: connections.Failing, Reason: "unknown mail capability"})
		}
	}
	if len(results) == 0 {
		return []connections.Result{{Capability: mailconn.CapabilityIMAP, State: connections.Unconfigured, Reason: "no mail capability is enabled"}}, nil
	}
	return results, nil
}

func candidateSpec(account string, cfg map[string]string) (config.AccountSpec, error) {
	enabled := true
	if value, ok := cfg["enabled"]; ok {
		enabled = value != "false"
	}
	spec, err := config.NewAccount(account, "", cfg["preset"], cfg["login"], enabled)
	if err != nil {
		return spec, err
	}
	if method := strings.TrimSpace(cfg["auth_method"]); method != "" {
		if spec.AuthMethod, err = config.ParseAuthMethod(method); err != nil {
			return spec, err
		}
	}
	if !config.AuthAllowed(spec.Preset, spec.AuthMethod) {
		return spec, errors.New("XOAUTH2 is Gmail-only")
	}
	return spec, nil
}

func testIMAP(ctx context.Context, lease *connections.Lease, spec config.AccountSpec, method config.AuthMethod, password string, source *gmailauth.Source) connections.Result {
	host := mailconn.IMAPHost(lease, spec)
	var err error
	if method == config.AuthXOAUTH2 {
		err = imapclient.CheckXOAUTH2(ctx, host, spec, source.Token, source.Invalidate)
	} else {
		err = imapclient.Check(ctx, host, spec, password)
	}
	if err != nil {
		return connections.Result{Capability: mailconn.CapabilityIMAP, State: classify(err), Reason: err.Error()}
	}
	return connections.Result{Capability: mailconn.CapabilityIMAP, State: connections.OK, Reason: "IMAP login and mailbox list work"}
}

func testSMTP(ctx context.Context, lease *connections.Lease, spec config.AccountSpec, method config.AuthMethod, password string, source *gmailauth.Source) connections.Result {
	host := mailconn.SMTPHost(lease, spec)
	var (
		token      func(context.Context) (string, error)
		invalidate func()
	)
	if method == config.AuthXOAUTH2 {
		token, invalidate = source.Token, source.Invalidate
	}
	if err := smtp.Probe(ctx, host, spec.Login, password, token, invalidate); err != nil {
		return connections.Result{Capability: mailconn.CapabilitySMTP, State: classify(err), Reason: err.Error()}
	}
	return connections.Result{Capability: mailconn.CapabilitySMTP, State: connections.OK, Reason: "SMTP TLS and authentication work"}
}

func classify(err error) connections.State {
	var refused *smtp.ResponseError
	if errors.As(err, &refused) && refused.Code >= 530 && refused.Code <= 535 {
		return connections.NeedsReauth
	}
	message := strings.ToLower(err.Error())
	for _, rejected := range []string{"authenticationfailed", "invalid credentials", "authentication failed", "auth failed", "invalid_grant", "unauthorized", "not accepted"} {
		if strings.Contains(message, rejected) {
			return connections.NeedsReauth
		}
	}
	return connections.Failing
}
