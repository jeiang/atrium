package oidcconn

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/auth/oidc"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
)

const Kind connections.Kind = "service/oidc"

func init() { oidc.UseConnections(source{}) }

func Connections() []connections.Definition {
	return []connections.Definition{{Kind: Kind, Validate: validate, Test: test}}
}

func validate(c connections.Candidate) error {
	return oidc.ValidateConnection(c.Config)
}

func test(ctx context.Context, lease *connections.Lease, caps []connections.Capability) ([]connections.Result, error) {
	want := map[string]bool{}
	for _, c := range caps {
		want[c.Name] = true
	}
	var out []connections.Result
	for _, r := range oidc.Probe(ctx, conn{lease}) {
		if len(want) > 0 && !want[r.Capability] {
			continue
		}
		out = append(out, connections.Result{
			Capability: r.Capability,
			State:      connections.State(r.State),
			Reason:     r.Reason,
			Data:       r.Data,
		})
	}
	return out, nil
}

func ImportV2(secret connections.ImportCredential) []string {
	destination := `connections."service/oidc".credential_files.client_secret`
	if strings.TrimSpace(secret.Value) == "" {
		return []string{"oidc client secret: no source secret; set " + destination + " in the host configuration"}
	}
	return []string{"oidc client secret: unresolved host credential; export the source secrets row to a secure file and set " + destination}
}

type source struct{}

func (source) Open(ctx context.Context, d *db.DB, id string) (oidc.Connection, error) {
	store := connections.DefaultFor(d.Read)
	if store == nil {
		reg, err := connections.From(ctx)
		if err != nil {
			return nil, err
		}
		store, err = connections.New(d, config.From(ctx).Host(), reg)
		if err != nil {
			return nil, err
		}
	}
	lease, err := store.Open(ctx, connections.ID(id))
	if err != nil {
		return nil, err
	}
	return conn{lease}, nil
}

type conn struct{ *connections.Lease }

func (c conn) Values() map[string]string {
	var m map[string]string
	if err := json.Unmarshal(c.Config(), &m); err != nil {
		return nil
	}
	return m
}

func (c conn) Credential(slot string) (string, error) {
	value, err := c.Lease.Credential(slot)
	var missing connections.MissingCredential
	if errors.As(err, &missing) {
		return "", oidc.ErrNoCredential
	}
	return value, err
}

func (c conn) Observe(ctx context.Context, result oidc.ProbeResult) error {
	return c.Lease.Observe(ctx, connections.Result{
		Capability: result.Capability,
		State:      connections.State(result.State),
		Reason:     result.Reason,
		Data:       result.Data,
	})
}
