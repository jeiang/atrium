package classifier

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/settings"
)

type Caller string

const (
	Folder      Caller = "mail_folder"
	Flag        Caller = "mail_flag"
	Event       Caller = "mail_event"
	Commitments Caller = "mail_commitments"
	Package     Caller = "mail_package"
	FolderName  Caller = "mail_folder_name"
	Ops         Caller = "ops_condenser"
)

const (
	OpsMonthlyCapKey = "ops_worker_monthly_usd"
	OpsRunCapKey     = "ops_worker_max_run_usd"
)

type definition struct {
	maxTokens int64
	keys      []string
	ops       bool
	reuse     bool
}

var definitions = map[Caller]definition{
	Folder:      {maxTokens: 80, keys: []string{"confident", "folder"}},
	Flag:        {maxTokens: 80, keys: []string{"deadline_or_payment_due", "flag", "person_asking", "security_event"}},
	Event:       {maxTokens: 80, keys: []string{"has_event"}},
	Commitments: {maxTokens: 500, keys: []string{"commitments"}},
	Package:     {maxTokens: 200, keys: []string{"carrier", "is_order", "merchant", "order_ref", "status", "status_text", "tracking_number"}},
	FolderName:  {maxTokens: 80, keys: []string{"name"}, reuse: true},
	Ops:         {maxTokens: 2000, keys: []string{"findings"}, ops: true},
}

func (d definition) wire(caller Caller, schema json.RawMessage) (json.RawMessage, error) {
	wire, err := anthropic.JSONOutputSchema(schema)
	if err != nil {
		return nil, fmt.Errorf("classifier: %s schema: %w", caller, err)
	}
	var root struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(wire, &root); err != nil {
		return nil, fmt.Errorf("classifier: %s schema: %w", caller, err)
	}
	if keys := slices.Sorted(maps.Keys(root.Properties)); !slices.Equal(keys, d.keys) {
		return nil, fmt.Errorf("classifier: %s schema has properties %v, want %v", caller, keys, d.keys)
	}
	return wire, nil
}

type caps struct {
	runUSD     *float64
	monthlyUSD *float64
}

func (d definition) caps(ctx context.Context, q db.Querier) (caps, error) {
	if !d.ops {
		return caps{}, nil
	}
	catalog, err := inference.RunCap(ctx, q, pricing.HaikuID)
	if err != nil {
		return caps{}, err
	}
	run, err := floatSetting(ctx, q, OpsRunCapKey)
	if err != nil {
		return caps{}, err
	}
	monthly, err := floatSetting(ctx, q, OpsMonthlyCapKey)
	if err != nil {
		return caps{}, err
	}
	limit := math.Min(catalog, run)
	return caps{runUSD: &limit, monthlyUSD: &monthly}, nil
}

func floatSetting(ctx context.Context, q db.Querier, key string) (float64, error) {
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	if err != nil {
		return 0, err
	}
	return found.Float()
}
