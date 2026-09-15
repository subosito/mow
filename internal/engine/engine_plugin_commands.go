package engine

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/subosito/mow/internal/contextload"
	"gopkg.in/yaml.v3"
)

// PluginCommand describes a prompt command provided by an installed plugin.
// Commands are engine-local; discovery never mutates the global slash registry.
type PluginCommand struct {
	Name        string
	Description string
	Plugin      string
	Skill       string
	Prompt      string
}

// PluginCommands discovers skill invocations and Markdown/TOML prompt commands.
// Plugin-qualified names always exist; the first plugin wins short-name conflicts.
func (e *Engine) PluginCommands() []PluginCommand {
	var out []PluginCommand
	seen := map[string]bool{}
	add := func(c PluginCommand) {
		for _, name := range []string{c.Plugin + ":" + c.Name, c.Name} {
			if !seen[name] {
				seen[name] = true
				copy := c
				copy.Name = name
				out = append(out, copy)
			}
		}
	}
	for _, p := range e.AvailablePlugins() {
		commands := map[string]PluginCommand{}
		for _, name := range p.SkillFolders {
			commands[name] = PluginCommand{Name: name, Plugin: p.ID, Skill: name, Description: "Run " + name + " skill", Prompt: "Use the " + name + " skill for this request."}
		}
		entries, _ := os.ReadDir(filepath.Join(p.Path, "commands"))
		for _, entry := range entries {
			extension := filepath.Ext(entry.Name())
			if entry.IsDir() || (extension != ".toml" && extension != ".md") {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), extension)
			if strings.ContainsAny(name, " \t\r\n:/\\") || strings.HasPrefix(name, ".") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(p.Path, "commands", entry.Name()))
			if err != nil || len(raw) > 128<<10 {
				continue
			}
			var spec struct {
				Description string `toml:"description" yaml:"description"`
				Prompt      string `toml:"prompt" yaml:"-"`
			}
			if extension == ".toml" {
				if toml.Unmarshal(raw, &spec) != nil {
					continue
				}
			} else {
				body := strings.ReplaceAll(string(raw), "\r\n", "\n")
				if strings.HasPrefix(body, "---\n") {
					front, rest, ok := strings.Cut(body[4:], "\n---\n")
					if !ok || yaml.Unmarshal([]byte(front), &spec) != nil {
						continue
					}
					body = rest
				}
				spec.Prompt = body
			}
			if strings.TrimSpace(spec.Prompt) == "" {
				continue
			}
			c := commands[name]
			c.Name, c.Plugin, c.Description, c.Prompt = name, p.ID, spec.Description, spec.Prompt
			commands[name] = c
		}
		names := slices.Sorted(maps.Keys(commands))
		for _, name := range names {
			add(commands[name])
		}
	}
	return out
}

// preparePluginCommand is called with promptMu held. Templates are instructions,
// never shell commands. Built-in slash handlers run before Engine.Prompt.
func (e *Engine) preparePluginCommand(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return text
	}
	for _, c := range e.PluginCommands() {
		if c.Name != strings.TrimPrefix(fields[0], "/") {
			continue
		}
		args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		prompt := c.Prompt
		if strings.Contains(prompt, "$ARGUMENTS") {
			prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", args)
		} else if args != "" {
			prompt += "\n\nUser arguments:\n" + args
		}
		if c.Skill != "" {
			loaded := contextload.LoadExplicitSkills(e.skillDirs, []string{c.Skill})
			e.mu.Lock()
			e.skillsText = mergeSkillText(e.skillsText, loaded)
			e.explicitSkills = mergeSkillNames(e.explicitSkills, []string{c.Skill})
			e.sys = e.recomposeSystemLocked()
			e.mu.Unlock()
		}
		return fmt.Sprintf("Plugin command /%s (%s):\n%s", c.Name, c.Plugin, prompt)
	}
	return text
}
