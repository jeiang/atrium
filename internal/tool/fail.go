package tool

import (
	"math"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/jsonx"
)

type failure struct {
	Code         application.Code `json:"code"`
	Message      string           `json:"message"`
	Fields       []failureField   `json:"fields,omitempty"`
	EffectIDs    []int64          `json:"effect_ids,omitempty"`
	RetryAfter   int64            `json:"retry_after,omitempty"`
	DiagnosticID string           `json:"diagnostic_id,omitempty"`
}

type failureField struct {
	Name    string `json:"name"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Fail(err error) error {
	e := application.Classify(err)
	out := failure{Code: e.Code, Message: e.Error(), EffectIDs: e.EffectIDs, DiagnosticID: e.DiagnosticID}
	if e.RetryAfter > 0 {
		out.RetryAfter = int64(math.Ceil(e.RetryAfter.Seconds()))
	}
	for _, f := range e.Fields {
		out.Fields = append(out.Fields, failureField(f))
	}
	return Error(jsonx.Dumps(out, jsonx.Opts{Compact: true, NoEnsureASCII: true}))
}
