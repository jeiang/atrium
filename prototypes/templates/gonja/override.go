package main

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/nikolalohinski/gonja/v2/exec"

	"prototype/templates/shared"
)

// override replaces builtin gonja filters whose output differs from Jinja's.
func override(f *exec.FilterSet) {
	// round: gonja rounds half away from zero (2.5 -> 3); Jinja/Python rounds half to even (2.5 -> 2).
	f.Replace("round", func(e *exec.Evaluator, in *exec.Value, p *exec.VarArgs) *exec.Value {
		if in.IsError() {
			return in
		}
		prec := 0
		if len(p.Args) > 0 {
			prec = p.Args[0].Integer()
		}
		scale := math.Pow10(prec)
		return exec.AsValue(math.RoundToEven(in.Float()*scale) / scale)
	})
	// urlencode: gonja leaves & and = unescaped ("é&=?" -> "%C3%A9&=%3F"); Jinja percent-encodes them.
	f.Replace("urlencode", func(e *exec.Evaluator, in *exec.Value, p *exec.VarArgs) *exec.Value {
		if in.IsError() {
			return in
		}
		return exec.AsValue(shared.URLEncode(in.String()))
	})
	// tojson: Jinja uses Python json.dumps(sort_keys=True) separators ", " / ": " and escapes < > & '.
	f.Replace("tojson", func(e *exec.Evaluator, in *exec.Value, p *exec.VarArgs) *exec.Value {
		if in.IsError() {
			return in
		}
		var b strings.Builder
		if err := pyJSON(&b, in.ToGoSimpleType(false)); err != nil {
			return exec.AsValue(err)
		}
		s := b.String()
		s = strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`, "'", `\u0027`).Replace(s)
		return exec.AsSafeValue(s)
	})
}

func pyJSON(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteString(": ")
			if err := pyJSON(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyJSON(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.WriteString(strings.TrimSpace(buf.String()))
	}
	return nil
}
