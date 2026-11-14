package apischema

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

const (
	OpenAPIFile = "openapi.json"
	ReachFile   = "reach.json"
)

var (
	pathParam  = regexp.MustCompile(`\{([^}:]+)(?::[^}]*)?\}`)
	errorRef   = object{"$ref": "#/components/schemas/Error"}
	guardSafe  = []int{400, 401, 403, 404, 429, 503}
	guardWrite = []int{409, 413, 415, 422}
)

var transportCodes = []string{
	"api_unavailable", "auth_unavailable", "bad_cursor", "bad_request", "command_interrupted", "command_result_withheld", "cross_origin", "idempotency_in_progress", "idempotency_mismatch", "invalid_token", "module_disabled", "no_active_run", "no_question", "not_answered", "nothing_to_edit", "project_archived", "read_only_thread", "reference_refused", "run_active", "stream_expired", "too_large", "unsupported_media_type",
}

func Generate(cat *module.Catalog) (map[string][]byte, error) {
	doc, err := openAPI(cat)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for name, v := range map[string]any{OpenAPIFile: doc, ReachFile: reach(cat)} {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return nil, err
		}
		out[name] = buf.Bytes()
	}
	return out, nil
}

func openAPI(cat *module.Catalog) (object, error) {
	w := newWalker()
	kinds := map[application.Name]application.Kind{}
	for _, op := range cat.Operations() {
		kinds[op.Name] = op.Kind
	}
	paths := object{}
	ids := map[string]string{}
	bindings := cat.Bindings()
	slices.SortFunc(bindings, func(a, b module.Binding) int { return cmp.Compare(a.Target, b.Target) })
	for _, b := range bindings {
		if b.API == nil {
			continue
		}
		method, pattern, _ := application.Route(b.Target)
		pattern = pathParam.ReplaceAllString(pattern, "{$1}")
		op, err := operation(w, b, kinds[b.Uses[0]], method)
		if err != nil {
			return nil, err
		}
		id := op["operationId"].(string)
		if other, ok := ids[id]; ok {
			return nil, fmt.Errorf("apischema: operationId %s is used by %s and %s", id, other, b.Target)
		}
		ids[id] = b.Target
		item, _ := paths[pattern].(object)
		if item == nil {
			item = object{}
			paths[pattern] = item
		}
		item[strings.ToLower(method)] = op
	}
	w.schemas["Error"] = errorSchema()
	return object{
		"openapi":    "3.1.0",
		"info":       object{"title": "atrium API", "version": "1"},
		"servers":    []any{object{"url": web.APIPrefix}},
		"paths":      paths,
		"components": object{"schemas": w.schemas},
	}, nil
}

func operation(w *walker, b module.Binding, kind application.Kind, method string) (object, error) {
	shape := b.API
	unsafe := method != http.MethodGet && method != http.MethodHead
	params := []any{}
	for _, f := range reflect.VisibleFields(shape.Request) {
		if f.Anonymous || !f.IsExported() {
			continue
		}
		for _, in := range []string{"path", "query", "header"} {
			name, ok := f.Tag.Lookup(in)
			if !ok {
				continue
			}
			schema, err := paramSchema(w, f.Type)
			if err != nil {
				return nil, err
			}
			p := object{"name": name, "in": in, "schema": constrain(schema, f.Tag.Get("api"))}
			if doc := f.Tag.Get("doc"); doc != "" {
				p["description"] = doc
			}
			if in == "path" {
				p["required"] = true
			}
			params = append(params, p)
		}
	}
	if unsafe && !shape.ReadOnlyPost {
		params = append(params, object{"name": "Idempotency-Key", "in": "header", "schema": object{"type": "string", "minLength": 1, "maxLength": 255}})
	} else if kind == application.Query && !shape.ReadOnlyPost && shape.Body != application.BodyEventStream && !shape.Uncached {
		params = append(params, object{"name": "If-None-Match", "in": "header", "schema": object{"type": "string"}})
	}
	id := string(b.Uses[0])
	if shape.ID != "" {
		id = shape.ID
	}
	op := object{
		"operationId":     id,
		"tags":            []any{string(shape.Area)},
		"x-atrium-area":   string(shape.Area),
		"x-atrium-module": b.Module,
		"parameters":      params,
	}
	if id != string(b.Uses[0]) {
		op["x-atrium-operation"] = string(b.Uses[0])
	}
	switch shape.Body {
	case application.BodyJSON:
		schema, err := w.ref(shape.Request)
		if err != nil {
			return nil, err
		}
		op["requestBody"] = object{"required": !shape.OptionalBody, "content": object{"application/json": object{"schema": schema}}}
	case application.BodyMultipart:
		schema, err := w.ref(shape.Request)
		if err != nil {
			return nil, err
		}
		multipartSchema := object{"allOf": []any{schema, object{"type": "object", "properties": object{"upload": object{"type": "array", "items": object{"type": "string", "format": "binary"}}}}}}
		if shape.MultipartFile != "" {
			file := shape.MultipartFile
			field := shape.MultipartAlternative
			multipartSchema = object{"type": "object", "properties": object{field: object{"type": "string"}, file: object{"type": "string", "format": "binary"}}, "oneOf": []any{object{"required": []string{field}, "not": object{"required": []string{file}}}, object{"required": []string{file}, "not": object{"required": []string{field}}}}}
		}
		content := object{"multipart/form-data": object{"schema": multipartSchema}}
		if shape.AllowJSON {
			content["application/json"] = object{"schema": schema}
		}
		op["requestBody"] = object{"required": true, "content": content}
	case application.BodyBinary:
		if unsafe {
			op["requestBody"] = object{"required": true, "content": object{"application/octet-stream": object{"schema": object{"type": "string", "contentMediaType": "application/octet-stream"}}}}
		}
	}
	headers := object{}
	if unsafe && !shape.ReadOnlyPost {
		headers["Atrium-Command-Id"] = object{"schema": object{"type": "string"}}
	} else if kind == application.Query && !shape.ReadOnlyPost && shape.Body != application.BodyEventStream && !shape.Uncached {
		headers["ETag"] = object{"schema": object{"type": "string"}}
	}
	success := object{"description": http.StatusText(shape.Success), "headers": headers}
	if shape.Success != http.StatusNoContent {
		schema, err := w.ref(shape.Response)
		if err != nil {
			return nil, err
		}
		media := "application/json"
		if shape.Body == application.BodyBinary {
			media = "application/octet-stream"
			schema = object{"type": "string", "format": "binary"}
		}
		if shape.Body == application.BodyEventStream {
			media = "text/event-stream"
		}
		success["content"] = object{media: object{"schema": schema}}
	}
	responses := object{strconv.Itoa(shape.Success): success}
	if shape.Body == application.BodyBinary {
		responses["206"] = success
		responses["416"] = object{"description": "Range Not Satisfiable"}
	}
	if shape.Body == application.BodyMultipart {
		op["x-atrium-body-limit"] = shape.BodyLimit
	}
	if shape.Body == application.BodyEventStream {
		responses["204"] = object{"description": http.StatusText(http.StatusNoContent)}
		responses["410"] = object{"description": http.StatusText(http.StatusGone), "content": object{"application/json": object{"schema": object{"allOf": []any{errorRef, object{
			"type": "object", "required": []any{"run_id"}, "properties": object{"run_id": object{"type": "integer"}},
		}}}}}}
	} else if kind == application.Query && !unsafe && !shape.Uncached {
		responses["304"] = object{"description": http.StatusText(http.StatusNotModified), "headers": object{"ETag": object{"schema": object{"type": "string"}}}}
	}
	statuses := slices.Clone(guardSafe)
	if unsafe {
		statuses = append(statuses, guardWrite...)
	}
	for _, code := range shape.Errors {
		statuses = append(statuses, web.Status(code))
	}
	for _, status := range statuses {
		resp := object{"description": http.StatusText(status), "content": object{"application/json": object{"schema": errorRef}}}
		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			resp["headers"] = object{"Retry-After": object{"schema": object{"type": "integer"}}}
		}
		responses[strconv.Itoa(status)] = resp
	}
	op["responses"] = responses
	return op, nil
}

func paramSchema(w *walker, t reflect.Type) (object, error) {
	switch t {
	case reflect.TypeFor[api.Cursor]():
		return object{"type": "string", "maxLength": api.CursorMax}, nil
	case reflect.TypeFor[api.Limit]():
		return object{"type": "integer", "minimum": 1, "maximum": api.LimitMax, "default": api.LimitDefault}, nil
	}
	return w.ref(t)
}

func errorSchema() object {
	codes := []any{}
	seen := map[string]bool{}
	for _, c := range application.Codes {
		seen[string(c)] = true
	}
	for _, c := range transportCodes {
		seen[c] = true
	}
	names := make([]string, 0, len(seen))
	for c := range seen {
		names = append(names, c)
	}
	slices.Sort(names)
	for _, c := range names {
		codes = append(codes, c)
	}
	str := object{"type": "string"}
	field := object{"type": "object", "required": []any{"type", "loc", "msg"}, "properties": object{
		"type": str, "loc": object{"type": "array", "items": str}, "msg": str, "input": object{}, "ctx": object{"type": "object"},
	}}
	return object{"type": "object", "required": []any{"error"}, "properties": object{"error": object{
		"type": "object", "required": []any{"code", "message"}, "properties": object{
			"code": object{"type": "string", "enum": codes}, "message": str,
			"fields":      object{"type": "array", "items": field},
			"effect_ids":  object{"type": "array", "items": object{"type": "integer", "format": "int64"}},
			"retry_after": object{"type": "integer"}, "diagnostic_id": str,
			"type": str, "status": object{"type": "integer"}, "instance": str, "command_id": str,
		},
	}}}
}
