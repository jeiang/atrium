package hub

func ResultString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		s, _ := x[key].(string)
		return s
	}
	return ""
}
