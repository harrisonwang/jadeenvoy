package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/harrisonwang/jadeenvoy/internal/sandbox"
	"github.com/harrisonwang/jadeenvoy/internal/store"
)

// mountSkills 解析 agent snapshot 中的 skills 配置，将 skill 文件写入沙箱，
// 返回要追加到 system prompt 的 SKILL.md 内容。
func (h *Harness) mountSkills(ctx context.Context, sb sandbox.Sandbox, agentSnapshot json.RawMessage) (string, error) {
	var snap struct {
		Skills json.RawMessage `json:"skills"`
	}
	_ = json.Unmarshal(agentSnapshot, &snap)
	if len(snap.Skills) == 0 || string(snap.Skills) == "null" || string(snap.Skills) == "[]" {
		return "", nil
	}

	type skillRef struct {
		Type    string `json:"type"`
		SkillID string `json:"skill_id"`
	}
	var refs []skillRef
	if err := json.Unmarshal(snap.Skills, &refs); err != nil {
		return "", err
	}

	var promptParts []string
	for _, ref := range refs {
		if ref.Type != "custom" || ref.SkillID == "" {
			continue
		}
		skill, err := h.Store.GetSkill(ctx, ref.SkillID)
		if err != nil {
			return "", fmt.Errorf("get skill %s: %w", ref.SkillID, err)
		}
		// 解析文件并写入沙箱
		var files []store.SkillFileEntry
		_ = json.Unmarshal(skill.FilesJSON, &files)
		skillDir := "/home/user/.skills/" + sanitizeName(skill.Name) + "/"
		for _, f := range files {
			if !safeRelativePath(f.Path) {
				return "", fmt.Errorf("unsafe skill file path: %s", f.Path)
			}
			mountPath := skillDir + f.Path
			if err := sb.WriteFile(ctx, mountPath, []byte(f.Content)); err != nil {
				return "", fmt.Errorf("write skill file %s: %w", f.Path, err)
			}
		}
		// 注入 SKILL.md 到 system prompt
		if skill.SkillMDContent != "" {
			promptParts = append(promptParts, "\n\n[Skill: "+skill.Name+"]\n"+skill.SkillMDContent)
		}
	}
	return strings.Join(promptParts, "\n"), nil
}

func safeRelativePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return false
	}
	clean := strings.ReplaceAll(p, `\\`, "/")
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
