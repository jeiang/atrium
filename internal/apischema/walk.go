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

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
	argPath  = regexp.MustCompile(`[\w./-]+\.(\w+)`)
)

type walker struct {
	schemas map[string]object
	names   map[string]reflect.Type
}

func newWalker() *walker {
	return &walker{schemas: map[string]object{}, names: map[string]reflect.Type{}}
}

func componentName(t reflect.Type) string {
	pkg := t.PkgPath()
	if strings.HasSuffix(pkg, "/application") {
		pkg = strings.TrimSuffix(pkg, "/application")
	}
	pkg = pkg[strings.LastIndex(pkg, "/")+1:]
	base, args, generic := strings.Cut(t.Name(), "[")
	name := capitalize(pkg) + base
	if generic {
		for _, m := range argPath.FindAllStringSubmatch(args, -1) {
			full := m[0]
			name += capitalize(full[strings.LastIndex(full, "/")+1 : strings.LastIndex(full, ".")])
			name += m[1]
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, name)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
		return object{"type": "integer", "format": "int64"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return object{"type": "integer"}, nil
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

func (w *walker) component(t reflect.Type, build func() (object, error)) (object, error) {
	name := componentName(t)
	ref := object{"$ref": "#/components/schemas/" + name}
	if prev, ok := w.names[name]; ok {
		if prev != t {
			return nil, fmt.Errorf("apischema: component %s names both %s and %s", name, prev, t)
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
		schema = constrain(schema, f.Tag.Get("api"))
		props[name] = schema
		if strings.Contains(","+f.Tag.Get("api")+",", ",required,") || f.Type.Kind() != reflect.Pointer && !strings.Contains(","+opts+",", ",omitempty,") && !strings.Contains(","+opts+",", ",omitzero,") {
			required = append(required, name)
		}
	}
	out := object{"type": "object", "properties": props, "additionalProperties": false}
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
