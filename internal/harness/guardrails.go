package harness

import (
	"fmt"

	"github.com/harrisonwang/jadeenvoy/pkg/apitypes"
)

func toolAllowedByGuardrails(guardrails *apitypes.AgentGuardrails, toolName string) (bool, string) {
	if guardrails == nil || guardrails.ToolPermissions == nil {
		return true, ""
	}
	policy := guardrails.ToolPermissions
	for _, denied := range policy.DeniedTools {
		if denied == toolName {
			return false, fmt.Sprintf("tool %q denied by agent guardrails", toolName)
		}
	}
	if len(policy.AllowedTools) == 0 {
		return true, ""
	}
	for _, allowed := range policy.AllowedTools {
		if allowed == toolName {
			return true, ""
		}
	}
	return false, fmt.Sprintf("tool %q is not in agent guardrails allowed_tools", toolName)
}
