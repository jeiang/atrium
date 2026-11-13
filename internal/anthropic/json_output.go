package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const FormatJSONSchema = "json_schema"

type JSONOutputFormat struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

type schemaNode struct {
	types       []string
	description string
	enum        []string
	properties  []string
	children    map[string]*schemaNode
	items       *schemaNode
	maxLength   int
	maxItems    int
}

type orderedObject struct {
	keys   []string
	values map[string]any
}

var schemaKeywords = map[string]bool{
	"type": true, "description": true, "enum": true, "properties": true, "required": true,
	"additionalProperties": true, "items": true, "maxLength": true, "maxItems": true,
}

func JSONOutputSchema(original json.RawMessage) (json.RawMessage, error) {
	root, err := compileSchema(original)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	root.emit(&buf)
	return buf.Bytes(), nil
}

func ValidateJSONOutput(original, output json.RawMessage) error {
	root, err := compileSchema(original)
	if err != nil {
		return err
	}
	value, err := decodeStrict(output)
	if err != nil {
		return fmt.Errorf("anthropic: json output: %w", err)
	}
	if err := root.check(value, "$"); err != nil {
		return fmt.Errorf("anthropic: json output: %w", err)
	}
	return nil
}

func compileSchema(raw json.RawMessage) (*schemaNode, error) {
	value, err := decodeStrict(raw)
	if err != nil {
		return nil, fmt.Errorf("anthropic: json schema: %w", err)
	}
	root, err := compileNode(value, "$")
	if err != nil {
		return nil, fmt.Errorf("anthropic: json schema: %w", err)
	}
	if !slices.Equal(root.types, []string{"object"}) {
		return nil, errors.New("anthropic: json schema: the root must be an object")
	}
	return root, nil
}

func compileNode(value any, path string) (*schemaNode, error) {
	obj, ok := value.(*orderedObject)
	if !ok {
		return nil, fmt.Errorf("%s: a schema must be an object", path)
	}
	for _, key := range obj.keys {
		if !schemaKeywords[key] {
			return nil, fmt.Errorf("%s: unsupported keyword %q", path, key)
		}
	}
	n := &schemaNode{}
	types, err := schemaTypes(obj.values["type"], path)
	if err != nil {
		return nil, err
	}
	n.types = types
	if d, present := obj.values["description"]; present {
		s, ok := d.(string)
		if !ok {
			return nil, fmt.Errorf("%s: description must be a string", path)
		}
		n.description = s
	}
	allowed := map[string]bool{"type": true, "description": true}
	switch {
	case slices.Equal(types, []string{"object"}):
		allowed["properties"], allowed["required"], allowed["additionalProperties"] = true, true, true
		if err := n.compileObject(obj, path); err != nil {
			return nil, err
		}
	case slices.Equal(types, []string{"array"}):
		allowed["items"], allowed["maxItems"] = true, true
		items, present := obj.values["items"]
		if !present {
			return nil, fmt.Errorf("%s: an array needs items", path)
		}
		if n.items, err = compileNode(items, path+"[]"); err != nil {
			return nil, err
		}
		if n.maxItems, err = positiveInt(obj.values, "maxItems", path); err != nil {
			return nil, err
		}
	case slices.Contains(types, "string"):
		allowed["maxLength"] = true
		if n.maxLength, err = positiveInt(obj.values, "maxLength", path); err != nil {
			return nil, err
		}
		if e, present := obj.values["enum"]; present {
			allowed["enum"] = true
			if !slices.Equal(types, []string{"string"}) {
				return nil, fmt.Errorf("%s: enum needs the single type string", path)
			}
			if n.enum, err = stringList(e, path+".enum"); err != nil || len(n.enum) == 0 {
				return nil, fmt.Errorf("%s: enum must be a nonempty list of distinct strings", path)
			}
		}
	}
	for _, key := range obj.keys {
		if !allowed[key] {
			return nil, fmt.Errorf("%s: keyword %q does not apply to type %s", path, key, strings.Join(types, "|"))
		}
	}
	return n, nil
}

func (n *schemaNode) compileObject(obj *orderedObject, path string) error {
	if obj.values["additionalProperties"] != false {
		return fmt.Errorf("%s: an object needs additionalProperties false", path)
	}
	props, ok := obj.values["properties"].(*orderedObject)
	if !ok || len(props.keys) == 0 {
		return fmt.Errorf("%s: an object needs nonempty properties", path)
	}
	required, err := stringList(obj.values["required"], path+".required")
	if err != nil {
		return err
	}
	if !slices.Equal(slices.Sorted(slices.Values(required)), slices.Sorted(slices.Values(props.keys))) {
		return fmt.Errorf("%s: required must list every property", path)
	}
	n.children = make(map[string]*schemaNode, len(props.keys))
	for _, key := range props.keys {
		child, err := compileNode(props.values[key], path+"."+key)
		if err != nil {
			return err
		}
		n.children[key] = child
	}
	n.properties = props.keys
	return nil
}

func schemaTypes(value any, path string) ([]string, error) {
	var list []string
	switch v := value.(type) {
	case string:
		list = []string{v}
	case []any:
		var err error
		if list, err = stringList(v, path+".type"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%s: type must be a string or a list of strings", path)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%s: type must not be empty", path)
	}
	for _, t := range list {
		switch t {
		case "object", "array":
			if len(list) > 1 {
				return nil, fmt.Errorf("%s: %s cannot be part of a type union", path, t)
			}
		case "string", "boolean", "null":
		default:
			return nil, fmt.Errorf("%s: unsupported type %q", path, t)
		}
	}
	return list, nil
}

func stringList(value any, path string) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: must be a list of strings", path)
	}
	out := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok || slices.Contains(out[:i], s) {
			return nil, fmt.Errorf("%s: must be a list of distinct strings", path)
		}
		out[i] = s
	}
	return out, nil
}

func positiveInt(values map[string]any, key, path string) (int, error) {
	raw, present := values[key]
	if !present {
		return 0, nil
	}
	num, ok := raw.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s: %s must be a positive integer", path, key)
	}
	n, err := strconv.Atoi(num.String())
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: %s must be a positive integer", path, key)
	}
	return n, nil
}

func (n *schemaNode) emit(buf *bytes.Buffer) {
	buf.WriteString(`{"type":`)
	if len(n.types) == 1 {
		writeJSONString(buf, n.types[0])
	} else {
		writeJSONStrings(buf, n.types)
	}
	if desc := n.wireDescription(); desc != "" {
		buf.WriteString(`,"description":`)
		writeJSONString(buf, desc)
	}
	if n.enum != nil {
		buf.WriteString(`,"enum":`)
		writeJSONStrings(buf, n.enum)
	}
	if n.children != nil {
		buf.WriteString(`,"properties":{`)
		for i, key := range n.properties {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, key)
			buf.WriteByte(':')
			n.children[key].emit(buf)
		}
		buf.WriteString(`},"required":`)
		writeJSONStrings(buf, n.properties)
		buf.WriteString(`,"additionalProperties":false`)
	}
	if n.items != nil {
		buf.WriteString(`,"items":`)
		n.items.emit(buf)
	}
	buf.WriteByte('}')
}

func (n *schemaNode) wireDescription() string {
	parts := []string{}
	if n.description != "" {
		parts = append(parts, n.description)
	}
	if n.maxLength > 0 {
		parts = append(parts, fmt.Sprintf("At most %d characters.", n.maxLength))
	}
	if n.maxItems > 0 {
		parts = append(parts, fmt.Sprintf("At most %d items.", n.maxItems))
	}
	return strings.Join(parts, " ")
}

func writeJSONString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Truncate(buf.Len() - 1)
}

func writeJSONStrings(buf *bytes.Buffer, list []string) {
	buf.WriteByte('[')
	for i, s := range list {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeJSONString(buf, s)
	}
	buf.WriteByte(']')
}

func (n *schemaNode) check(value any, path string) error {
	switch v := value.(type) {
	case nil:
		if !slices.Contains(n.types, "null") {
			return fmt.Errorf("%s: unexpected null", path)
		}
	case bool:
		if !slices.Contains(n.types, "boolean") {
			return fmt.Errorf("%s: unexpected boolean", path)
		}
	case string:
		if !slices.Contains(n.types, "string") {
			return fmt.Errorf("%s: unexpected string", path)
		}
		if n.enum != nil && !slices.Contains(n.enum, v) {
			return fmt.Errorf("%s: value is not in the enum", path)
		}
		if n.maxLength > 0 && utf8.RuneCountInString(v) > n.maxLength {
			return fmt.Errorf("%s: longer than %d characters", path, n.maxLength)
		}
	case []any:
		if n.items == nil {
			return fmt.Errorf("%s: unexpected array", path)
		}
		if n.maxItems > 0 && len(v) > n.maxItems {
			return fmt.Errorf("%s: more than %d items", path, n.maxItems)
		}
		for i, item := range v {
			if err := n.items.check(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case *orderedObject:
		if n.children == nil {
			return fmt.Errorf("%s: unexpected object", path)
		}
		for _, key := range v.keys {
			if n.children[key] == nil {
				return fmt.Errorf("%s: unexpected key %q", path, key)
			}
		}
		for _, key := range n.properties {
			item, present := v.values[key]
			if !present {
				return fmt.Errorf("%s: missing key %q", path, key)
			}
			if err := n.children[key].check(item, path+"."+key); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unexpected number", path)
	}
	return nil
}

func decodeStrict(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the JSON value")
	}
	return value, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		obj := &orderedObject{values: map[string]any{}}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key := keyTok.(string)
			if _, dup := obj.values[key]; dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj.keys = append(obj.keys, key)
			obj.values[key] = value
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case json.Delim('['):
		list := []any{}
		for dec.More() {
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return list, nil
	default:
		return tok, nil
	}
}
