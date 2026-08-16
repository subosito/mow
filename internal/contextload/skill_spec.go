package contextload

import (
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// SkillInfo is one Agent Skills entry (https://agentskills.io/specification):
// a folder with SKILL.md, optional YAML frontmatter, markdown body.
//
// Agent Plugins (https://agent-plugins.org/specification) can ship a
// skills/ directory of these later via /plugins. This type is the skill
// unit only — not a plugin bundle.
type SkillInfo struct {
	// Name is the spec `name` when valid, otherwise the folder name.
	Name string
	// Folder is the on-disk directory name (ActivateSkills match key).
	Folder string
	// Description is the spec `description` (listing / discovery).
	Description string
	// DisableModelInvocation is the spec `disable-model-invocation`.
	// When true the first-prompt selector must not auto-load this skill.
	DisableModelInvocation bool
	// Body is SKILL.md after frontmatter. Empty means do not inject.
	Body string
}

type skillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

// parseSkillMarkdown splits optional YAML frontmatter from the instruction body.
// A missing or invalid fence is treated as a body-only skill (folder name wins).
func parseSkillMarkdown(folder, raw string) SkillInfo {
	info := SkillInfo{Name: folder, Folder: folder}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return info
	}
	fm, body, ok := splitFrontmatter(raw)
	if !ok {
		info.Body = raw
		return info
	}
	var meta skillFrontmatter
	if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
		// Spec says ignore unknown fields; invalid YAML still yields the body
		// so a typo in the fence does not hide the skill.
		info.Body = strings.TrimSpace(body)
		if info.Body == "" {
			info.Body = raw
		}
		return info
	}
	if name := specSkillName(meta.Name); name != "" {
		info.Name = name
	}
	info.Description = strings.TrimSpace(meta.Description)
	info.DisableModelInvocation = meta.DisableModelInvocation
	info.Body = strings.TrimSpace(body)
	return info
}

func splitFrontmatter(raw string) (front, body string, ok bool) {
	s := raw
	if strings.HasPrefix(s, "\ufeff") {
		s = strings.TrimPrefix(s, "\ufeff")
	}
	if !strings.HasPrefix(s, "---") {
		return "", raw, false
	}
	rest := s[3:]
	if rest != "" && rest[0] != '\n' && !strings.HasPrefix(rest, "\r\n") {
		return "", raw, false
	}
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")
	// Close on a line that is only --- (optional trailing space).
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", raw, false
	}
	front = rest[:idx]
	after := rest[idx+len("\n---"):]
	after = strings.TrimPrefix(after, "\r")
	if after != "" && after[0] != '\n' && !isOnlySpace(after) {
		// `---something` is not a closer.
		return "", raw, false
	}
	after = strings.TrimPrefix(after, "\n")
	return front, after, true
}

func isOnlySpace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// specSkillName accepts Agent Skills `name`: 1–64 chars, lowercase
// letters, digits, and hyphens.
func specSkillName(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return ""
	}
	return s
}

func formatSkillSection(info SkillInfo) string {
	body := strings.TrimSpace(info.Body)
	if body == "" {
		return ""
	}
	label := info.Name
	if label == "" {
		label = info.Folder
	}
	return "## skill: " + label + "\n\n" + body
}
