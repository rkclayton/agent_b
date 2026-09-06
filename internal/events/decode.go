package events

import "encoding/json"

func decodeValue(value, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func valueMap(value any) map[string]any { result, _ := value.(map[string]any); return result }
func valueString(value any) string      { result, _ := value.(string); return result }
func valueBool(value any) bool          { result, _ := value.(bool); return result }
func valueStrings(value any) []string {
	if values, ok := value.([]string); ok {
		return append([]string(nil), values...)
	}
	raw, _ := value.([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, valueString(item))
	}
	return out
}
