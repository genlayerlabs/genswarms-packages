package ir

// Helpers for parsing JSON-decoded maps (string keys, values as decoded by
// encoding/json: objects → map[string]any, numbers → float64, arrays → []any).

func asString(m map[string]any, k string) (string, bool) {
	s, ok := m[k].(string)
	return s, ok && s != ""
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

// asInt reports the integer value of a JSON number, and whether v was an
// integral number (json decodes all numbers to float64).
func asInt(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), f == float64(int(f))
}

// asStringMap returns m[k] as a map, defaulting to an empty map when absent.
func asStringMap(m map[string]any, k string) map[string]any {
	if mm, ok := m[k].(map[string]any); ok {
		return mm
	}
	return map[string]any{}
}
