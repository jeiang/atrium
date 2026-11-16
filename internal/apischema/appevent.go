package apischema

const appEvent = "AppEvent"

func (w *walker) appEvent() object {
	str := object{"type": "string"}
	topic := object{"type": "object", "required": []any{"topic", "revision"}, "properties": object{"topic": str, "revision": w.integer(true)}}
	events := []struct {
		name string
		data object
	}{
		{"sync", object{"type": "object", "required": []any{"topics"}, "properties": object{"topics": object{"type": "array", "items": topic}}}},
		{"change", topic},
	}
	mapping := object{}
	variants := []any{}
	for _, e := range events {
		name := appEvent + camel(e.name)
		w.schemas[name] = object{"type": "object", "required": []any{"event", "data"}, "properties": object{
			"event": object{"type": "string", "const": e.name},
			"data":  object{"type": "string", "contentMediaType": "application/json", "contentSchema": e.data},
		}}
		ref := "#/components/schemas/" + name
		mapping[e.name] = ref
		variants = append(variants, object{"$ref": ref})
	}
	w.schemas[appEvent] = object{"anyOf": []any{
		object{"oneOf": variants, "discriminator": object{"propertyName": "event", "mapping": mapping}},
		object{"type": "object", "required": []any{"event", "data"}, "properties": object{"event": str, "data": object{"type": "string", "contentMediaType": "application/json"}}},
	}}
	return object{"$ref": "#/components/schemas/" + appEvent}
}
