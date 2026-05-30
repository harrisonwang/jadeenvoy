package harness

import "encoding/json"

// parseModelID 从 raw model JSON 中取 id 字段。
func parseModelID(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.ID
}
