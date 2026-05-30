package provider

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// MockScript 描述一轮"如果输入 message 满足条件，回什么 events"。
type MockScript struct {
	// Match 是函数：检查最后一条 user/tool_result message，决定是否触发本条 script
	Match func(req ChatRequest) bool

	// Events 按序发出，最后必须以 StopReason 结束
	Events []ChatEvent
}

// MockProvider 用脚本化响应实现 Provider，供测试用。
type MockProvider struct {
	mu      sync.Mutex
	scripts []MockScript
	called  int // 已经调用过的次数
}

func NewMockProvider() *MockProvider {
	return &MockProvider{}
}

// Append 加一条脚本（按 Append 顺序匹配；第一个 Match 命中就用）。
func (m *MockProvider) Append(s MockScript) *MockProvider {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts = append(m.scripts, s)
	return m
}

// AppendText 是 helper：无条件返回文本 + end_turn。
func (m *MockProvider) AppendText(text string) *MockProvider {
	return m.Append(MockScript{
		Match: func(req ChatRequest) bool { return true },
		Events: []ChatEvent{
			TextDelta{Text: text},
			StopReason{Reason: "end_turn", Usage: Usage{InputTokens: 10, OutputTokens: int64(len(text))}},
		},
	})
}

// AppendToolUse 是 helper：用 tool_use 调一个 bash 命令，然后结束 turn 等待 tool_result。
func (m *MockProvider) AppendToolUse(toolName string, input map[string]any) *MockProvider {
	inputJSON, _ := json.Marshal(input)
	return m.Append(MockScript{
		Match: func(req ChatRequest) bool {
			// 仅在 history 中没有 tool_result 时使用
			for _, msg := range req.Messages {
				for _, b := range msg.Content {
					if b.Type == "tool_result" {
						return false
					}
				}
			}
			return true
		},
		Events: []ChatEvent{
			ToolUseStart{ID: "call_mock_001", Name: toolName},
			ToolUseDelta{ID: "call_mock_001", InputJSON: string(inputJSON)},
			StopReason{Reason: "tool_use", Usage: Usage{InputTokens: 20, OutputTokens: 5}},
		},
	})
}

// AppendFinalAfterTool 是 helper：在收到 tool_result 后给一段文本结束。
func (m *MockProvider) AppendFinalAfterTool(text string) *MockProvider {
	return m.Append(MockScript{
		Match: func(req ChatRequest) bool {
			for _, msg := range req.Messages {
				for _, b := range msg.Content {
					if b.Type == "tool_result" {
						return true
					}
				}
			}
			return false
		},
		Events: []ChatEvent{
			TextDelta{Text: text},
			StopReason{Reason: "end_turn", Usage: Usage{InputTokens: 30, OutputTokens: int64(len(text))}},
		},
	})
}

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) Stream(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called++

	// 找匹配的 script
	var chosen *MockScript
	for i := range m.scripts {
		if m.scripts[i].Match(req) {
			chosen = &m.scripts[i]
			break
		}
	}
	ch := make(chan ChatEvent, 8)
	go func() {
		defer close(ch)
		if chosen == nil {
			// 默认：返回"无脚本匹配"错误事件 + end_turn 防 harness 死循环
			text := "[mock] no matching script for request"
			ch <- TextDelta{Text: text}
			ch <- StopReason{Reason: "end_turn", Usage: Usage{InputTokens: 0, OutputTokens: int64(len(text))}}
			return
		}
		for _, ev := range chosen.Events {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

// CalledCount 测试用，看调用次数。
func (m *MockProvider) CalledCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.called
}

// 防 unused
var _ = strings.HasPrefix
