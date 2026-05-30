package e2e

import "github.com/harrisonwang/jadeenvoy/internal/provider"

// 别名（避免 m2_test.go 引用过多 provider. 前缀）
type (
	provider_MockScript   = provider.MockScript
	provider_ChatRequest  = provider.ChatRequest
	provider_ChatEvent    = provider.ChatEvent
	provider_ToolUseStart = provider.ToolUseStart
	provider_ToolUseDelta = provider.ToolUseDelta
	provider_StopReason   = provider.StopReason
	provider_Usage        = provider.Usage
)
