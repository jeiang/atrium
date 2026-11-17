package apischema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/application"
)

type object = map[string]any

const maxSafeInteger int64 = 1<<53 - 1

var (
	timeType  = reflect.TypeFor[time.Time]()
	rawType   = reflect.TypeFor[json.RawMessage]()
	int64Type = reflect.TypeFor[int64]()
	argPath   = regexp.MustCompile(`[\w./-]+\.(\w+)`)
)

type walker struct {
	schemas map[string]object
	names   map[string]reflect.Type
	outputs map[reflect.Type]bool
	input   bool
}

func newWalker() *walker {
	return &walker{schemas: map[string]object{}, names: map[string]reflect.Type{}, outputs: map[reflect.Type]bool{}}
}

const atrium = "github.com/jeiang/atrium/internal/"

var pinnedNames = map[string]string{
	atrium + "apps/calendar.Event":                     "CalendarEvent",
	atrium + "apps/calendar.Source":                    "CalendarSource",
	atrium + "assistant/assistantmodule.apiAttachment": "ThreadAttachment",
	atrium + "apps/mail/message.Attachment":            "MailAttachment",
	atrium + "apps/packages/tracking.Result":           "TrackingResult",
	atrium + "apps/packages/tracking.Event":            "TrackingEvent",
	atrium + "apps/mail/live.Action":                   "LiveAction",
	atrium + "attention.Action":                        "AttentionAction",
	atrium + "assistant/assistantmodule.apiAbout":      "AboutPage",
	atrium + "assistant/assistantmodule.apiAgenda":     "AgendaPage",
	atrium + "assistant/assistantmodule.apiItem":       "TranscriptItem",
	atrium + "apps/library.Channel":                    "MediaChannel",
	atrium + "notify.Channel":                          "NotificationChannel",
	atrium + "notify/application.KindRow":              "NotificationKindRow",
	atrium + "inference.KindRow":                       "InferenceKindRow",
	atrium + "inference.Status":                        "InferenceStatus",
	atrium + "inference.Summary":                       "InferenceSummary",
	atrium + "assistant/memory.Line":                   "MemoryLine",
	atrium + "apps/mail/replay.Line":                   "ReplayLine",
	atrium + "assistant/threads.Listed":                "ThreadListing",
	atrium + "attention.Result":                        "AttentionResult",
	atrium + "tasks.Link":                              "TaskLink",
	atrium + "tasks.Counts":                            "TaskCounts",
	atrium + "connections.Result":                      "ConnectionResult",
	atrium + "connections.View":                        "ConnectionView",
	atrium + "application/commands.Status":             "CommandStatus",
	atrium + "apps/mail/live.Status":                   "LiveStatus",
	atrium + "assistant/files.Summary":                 "FileSummary",
	atrium + "homecard.Weather":                        "HomeWeather",
}

func componentName(t reflect.Type) string {
	base, args, generic := strings.Cut(t.Name(), "[")
	name := typeName(t.PkgPath() + "." + base)
	if generic {
		for _, m := range argPath.FindAllString(args, -1) {
			name += typeName(m)
		}
	}
	return name
}

func typeName(full string) string {
	if pinned, ok := pinnedNames[full]; ok {
		return pinned
	}
	base := full[strings.LastIndex(full, ".")+1:]
	if rest, ok := strings.CutPrefix(base, "api"); ok && rest != "" && unicode.IsUpper(rune(rest[0])) {
		base = rest
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, capitalize(base))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (w *walker) collect(t reflect.Type) {
	if t == timeType || t == rawType {
		return
	}
	if u, ok := application.UnionOf(t); ok {
		if w.outputs[t] {
			return
		}
		w.outputs[t] = true
		for _, kind := range u.Kinds() {
			w.collect(u.Variants[kind])
		}
		return
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
		w.collect(t.Elem())
	case reflect.Struct:
		if t.Name() != "" {
			if w.outputs[t] {
				return
			}
			w.outputs[t] = true
		}
		for _, f := range reflect.VisibleFields(t) {
			if !f.Anonymous && f.IsExported() && f.Tag.Get("json") != "-" {
				w.collect(f.Type)
			}
		}
	}
}

func (w *walker) integer(format bool) object {
	out := object{"type": "integer"}
	if format {
		out["format"] = "int64"
	}
	if !w.input {
		out["maximum"] = maxSafeInteger
	}
	return out
}

func (w *walker) ref(t reflect.Type) (object, error) {
	switch t {
	case timeType:
		return object{"type": "string", "format": "date-time"}, nil
	case rawType:
		return object{}, nil
	}
	if u, ok := application.UnionOf(t); ok {
		return w.union(t, u)
	}
	switch t.Kind() {
	case reflect.Pointer:
		return w.ref(t.Elem())
	case reflect.String:
		return object{"type": "string"}, nil
	case reflect.Bool:
		return object{"type": "boolean"}, nil
	case reflect.Int64, reflect.Uint64:
		return w.integer(true), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return w.integer(false), nil
	case reflect.Float32, reflect.Float64:
		return object{"type": "number"}, nil
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return object{"type": "string", "contentEncoding": "base64"}, nil
		}
		items, err := w.ref(t.Elem())
		if err != nil {
			return nil, err
		}
		return object{"type": "array", "items": items}, nil
	case reflect.Map:
		values, err := w.ref(t.Elem())
		if err != nil {
			return nil, err
		}
		return object{"type": "object", "additionalProperties": values}, nil
	case reflect.Struct:
		if t.Name() == "" {
			return w.structSchema(t)
		}
		return w.component(t, func() (object, error) { return w.structSchema(t) })
	}
	return nil, fmt.Errorf("apischema: unsupported type %s", t)
}

var typeRules = map[string]reflect.Type{
	"string":  reflect.TypeFor[string](),
	"integer": int64Type,
	"object":  reflect.TypeFor[map[string]json.RawMessage](),
}

func typeRule(tag string) (reflect.Type, error) {
	for part := range strings.SplitSeq(tag, ",") {
		if key, value, _ := strings.Cut(part, "="); key == "type" {
			if t, ok := typeRules[value]; ok {
				return t, nil
			}
			return nil, fmt.Errorf("apischema: unknown type rule %q", value)
		}
	}
	return nil, nil
}

func (w *walker) component(t reflect.Type, build func() (object, error)) (object, error) {
	name := componentName(t)
	if w.input && w.outputs[t] {
		name += "Input"
	}
	ref := object{"$ref": "#/components/schemas/" + name}
	if prev, ok := w.names[name]; ok {
		if prev != t {
			return nil, fmt.Errorf("apischema: component %s names both %s and %s; pin a name for the new type in pinnedNames", name, prev, t)
		}
		return ref, nil
	}
	w.names[name] = t
	schema, err := build()
	if err != nil {
		return nil, err
	}
	w.schemas[name] = schema
	return ref, nil
}

func (w *walker) union(t reflect.Type, u application.Union) (object, error) {
	return w.component(t, func() (object, error) {
		mapping := object{}
		var variants []any
		for _, kind := range u.Kinds() {
			ref, err := w.ref(u.Variants[kind])
			if err != nil {
				return nil, err
			}
			mapping[kind] = ref["$ref"]
			variants = append(variants, ref)
		}
		return object{"oneOf": variants, "discriminator": object{"propertyName": application.UnionDiscriminator, "mapping": mapping}}, nil
	})
}

func (w *walker) structSchema(t reflect.Type) (object, error) {
	props := object{}
	required := []any{}
	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous || !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		schema, err := w.ref(f.Type)
		if err != nil {
			return nil, err
		}
		rule, err := typeRule(f.Tag.Get("api"))
		if err != nil {
			return nil, err
		}
		switch {
		case rule == nil:
		case f.Type == rawType:
			schema, err = w.ref(rule)
		case f.Type == reflect.SliceOf(rawType):
			schema, err = w.ref(reflect.SliceOf(rule))
		default:
			err = fmt.Errorf("apischema: type rule on %s.%s, which is not raw JSON", t, f.Name)
		}
		if err != nil {
			return nil, err
		}
		omitted := strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
		apiTag := f.Tag.Get("api")
		schema = constrain(schema, apiTag)
		if !w.input && f.Type.Kind() == reflect.Pointer && !omitted && !strings.Contains(","+apiTag+",", ",nullable,") {
			schema = object{"anyOf": []any{schema, object{"type": "null"}}}
		}
		props[name] = schema
		if strings.Contains(","+apiTag+",", ",required,") || f.Type.Kind() != reflect.Pointer && !omitted {
			required = append(required, name)
		}
	}
	out := object{"type": "object", "properties": props}
	if w.input {
		out["additionalProperties"] = false
	}
	if len(required) > 0 {
		out["required"] = required
	}
	if kind, ok := application.VariantKind(t); ok {
		props[application.UnionDiscriminator] = object{"type": "string", "const": kind}
	}
	return out, nil
}

func constrain(schema object, tag string) object {
	if tag == "" {
		return schema
	}
	out := object{}
	for k, v := range schema {
		out[k] = v
	}
	nullable := false
	for part := range strings.SplitSeq(tag, ",") {
		key, value, _ := strings.Cut(part, "=")
		switch key {
		case "min", "max":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				continue
			}
			out[map[string]string{"min": "minimum", "max": "maximum"}[key]] = n
		case "enum":
			var values []any
			for _, v := range strings.Split(value, "|") {
				values = append(values, v)
			}
			out["enum"] = values
		case "nullable":
			nullable = true
		case "writeonly":
			out["writeOnly"] = true
		}
	}
	if nullable {
		return object{"anyOf": []any{out, object{"type": "null"}}}
	}
	return out
}
