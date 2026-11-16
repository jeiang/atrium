package apischema

import (
	"slices"
	"strings"
)

const runStreamEvent = "RunStreamEvent"

type streamField struct {
	name   string
	schema object
}

func (w *walker) runStreamEvent() object {
	str := object{"type": "string"}
	id := w.integer(true)
	nullable := func(s object) object { return object{"anyOf": []any{s, object{"type": "null"}}} }
	text := []streamField{{"text", str}}
	events := []struct {
		name    string
		durable bool
		fields  []streamField
	}{
		{"run_start", true, []streamField{{"thread_id", id}, {"thread_kind", str}, {"role", nullable(str)}, {"title", nullable(str)}, {"effort", str}, {"parent_run_id", nullable(id)}}},
		{"thinking", true, text},
		{"text", true, text},
		{"tool_start", true, []streamField{{"id", str}, {"name", str}, {"args", str}}},
		{"tool_done", true, []streamField{{"id", str}, {"ok", object{"type": "boolean"}}, {"preview", str}}},
		{"user", true, []streamField{{"text", str}, {"queue_id", id}}},
		{"plan", true, []streamField{{"items", object{"type": "array", "items": object{"type": "object", "required": []any{"text", "done"}, "properties": object{"text": str, "done": object{"type": "boolean"}}}}}}},
		{"run_done", true, []streamField{{"state", str}, {"error", nullable(str)}}},
		{"complete", true, []streamField{{"state", object{"type": "string", "const": "done"}}, {"thread_id", id}}},
		{"run_error", true, []streamField{{"state", object{"type": "string", "const": "failed"}}, {"problem", object{"type": "object", "required": []any{"code", "message"}, "properties": object{"code": str, "message": str}}}, {"effect_ids", object{"type": "array", "items": id}}}},
		{"done", false, []streamField{{"state", str}}},
	}
	mapping := object{}
	variants := []any{}
	for _, e := range events {
		props := object{}
		required := []any{}
		if e.durable {
			props["run_id"] = id
			required = append(required, "run_id")
		}
		for _, f := range e.fields {
			props[f.name] = f.schema
			required = append(required, f.name)
		}
		frame := object{
			"event": object{"type": "string", "const": e.name},
			"data":  object{"type": "object", "required": required, "properties": props},
		}
		frameRequired := []any{"event", "data"}
		if e.durable {
			frame["id"] = object{"type": "string", "pattern": "^[1-9][0-9]*$"}
			frameRequired = slices.Insert(frameRequired, 1, any("id"))
		}
		name := runStreamEvent + camel(e.name)
		w.schemas[name] = object{"type": "object", "required": frameRequired, "properties": frame}
		ref := "#/components/schemas/" + name
		mapping[e.name] = ref
		variants = append(variants, object{"$ref": ref})
	}
	w.schemas[runStreamEvent] = object{"anyOf": []any{
		object{"oneOf": variants, "discriminator": object{"propertyName": "event", "mapping": mapping}},
		object{"type": "object", "required": []any{"event", "data"}, "properties": object{"event": str, "id": str, "data": object{"type": "object"}}},
	}}
	return object{"$ref": "#/components/schemas/" + runStreamEvent}
}

func camel(s string) string {
	out := ""
	for part := range strings.SplitSeq(s, "_") {
		out += capitalize(part)
	}
	return out
}
