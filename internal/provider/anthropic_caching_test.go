package provider

import (
	"encoding/json"
	"testing"
)

// TestBuildAnthropicRequest_PromptCaching 验证开启缓存时：
//   - system 转成带 cache_control: ephemeral 的 block 数组
//   - 只有最后一个 tool 带 cache_control（缓存整个 tools 前缀）
func TestBuildAnthropicRequest_PromptCaching(t *testing.T) {
	req := ChatRequest{
		Model:  "claude-opus-4-8",
		System: "You are a helpful assistant.",
		Messages: []Message{
			{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
		Tools: []ToolDef{
			{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}

	out := buildAnthropicRequest(req, true)

	// 重新序列化再解析，断言线缆上的真实 JSON 形状。
	raw, _ := json.Marshal(out)
	var parsed struct {
		System []struct {
			Type         string         `json:"type"`
			Text         string         `json:"text"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"system"`
		Tools []struct {
			Name         string         `json:"name"`
			CacheControl map[string]any `json:"cache_control"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
	}

	if len(parsed.System) != 1 {
		t.Fatalf("expected system as 1-block array, got %d blocks: %s", len(parsed.System), raw)
	}
	if parsed.System[0].Text != "You are a helpful assistant." {
		t.Fatalf("system text mismatch: %q", parsed.System[0].Text)
	}
	if parsed.System[0].CacheControl["type"] != "ephemeral" {
		t.Fatalf("expected system cache_control ephemeral, got %v", parsed.System[0].CacheControl)
	}

	if len(parsed.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(parsed.Tools))
	}
	if parsed.Tools[0].CacheControl != nil {
		t.Fatalf("expected NO cache_control on first tool, got %v", parsed.Tools[0].CacheControl)
	}
	if parsed.Tools[1].CacheControl["type"] != "ephemeral" {
		t.Fatalf("expected cache_control ephemeral on last tool, got %v", parsed.Tools[1].CacheControl)
	}
}

// TestBuildAnthropicRequest_CachingDisabled 验证关闭缓存时 system 退回纯字符串、
// 工具不带 cache_control —— 给可能不支持 cache_control 的 anthropic_compat 网关留退路。
func TestBuildAnthropicRequest_CachingDisabled(t *testing.T) {
	req := ChatRequest{
		Model:    "m",
		System:   "sys",
		Messages: []Message{{Role: RoleUser, Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Tools:    []ToolDef{{Name: "bash", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}

	out := buildAnthropicRequest(req, false)
	raw, _ := json.Marshal(out)

	var parsed struct {
		System any `json:"system"`
		Tools  []struct {
			CacheControl map[string]any `json:"cache_control"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s, ok := parsed.System.(string); !ok || s != "sys" {
		t.Fatalf("expected system as plain string %q, got %T %v", "sys", parsed.System, parsed.System)
	}
	if parsed.Tools[0].CacheControl != nil {
		t.Fatalf("expected no cache_control when disabled, got %v", parsed.Tools[0].CacheControl)
	}
}
