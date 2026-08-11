package mowi

import (
	"context"
	"fmt"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/slash"
)

// /skill is a discoverability and activation surface for the interactive
// session. With no arguments it lists the skill folder names available across
// the engine's configured skill dirs (global, user, trusted project) without
// injecting their bodies into the prompt. With one or more names it activates
// those skills immediately for subsequent turns via Engine.ActivateSkills,
// which merges them into the live system prompt (no restart needed) and
// preserves the first-prompt selector and explicit CLI/config skills.
//
// This keeps the trust model intact: /skill never reads skill bodies into the
// transcript, and it never bypasses the selector — activation reuses the same
// LoadExplicitSkills path that --skill uses, just at runtime.
func init() {
	slash.Register(slash.Command{
		Name:    "skill",
		Summary: "list skills (`/skill`) or activate them for subsequent turns (`/skill name...`)",
		Usage: `/skill — list discoverable skills
/skill <name> [<name>...] — activate skills for subsequent turns

With no arguments, prints the skill folder names that contain a SKILL.md entry
point across the session's configured skill directories (global $MOW_HOME/skills,
user skills.dirs, and trusted project .mow/skills). Bodies are not shown.

With one or more names, activates those skills immediately: they are loaded
into the live system prompt for subsequent turns (no restart), merged with any
skills the first-prompt selector already matched and any explicit --skill /
skills.explicit names. Activation is idempotent — re-activating an already-
loaded skill is a no-op for the prompt.

Unknown names are reported but do not abort the good ones.

You can also load skills at startup:

  mow --skill <name>          # one skill (repeatable)
  mow --skill docker --skill go

Or set skills.explicit in config:

  skills:
    explicit: [docker, go]

When skills.selector is on (default), a skill also loads when its folder name
appears in your first prompt — /skill just tells you what names exist.`,
		Run: func(ctx context.Context, req slash.Request) (slash.Result, error) {
			eng := req.Engine
			if eng == nil {
				return slash.Result{}, fmt.Errorf("skill: no engine in session")
			}
			args := req.Args
			if len(args) == 0 {
				return listSkillsResult(eng), nil
			}
			// Activate named skills for subsequent turns.
			activated, unknown := eng.ActivateSkills(args...)
			return activateSkillsResult(eng, args, activated, unknown), nil
		},
	})
}

// listSkillsResult builds the /skill (no-args) listing from the engine's
// actual available skill set.
func listSkillsResult(eng *mow.Engine) slash.Result {
	names := eng.AvailableSkills()
	title := fmt.Sprintf("skill · %d available", len(names))
	if len(names) == 0 {
		return slash.Result{
			Title: title,
			Body: "No skills found in the configured skill directories.\n\n" +
				"Put folders with SKILL.md under $MOW_HOME/skills/<name>/ to make them discoverable.",
		}
	}
	var b strings.Builder
	b.WriteString("Available skills (activate with `/skill <name>` or restart with `--skill <name>`):\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  • %s\n", n)
	}
	b.WriteString("\n`/skill <name>` activates immediately for subsequent turns.")
	return slash.Result{Title: title, Body: b.String()}
}

// activateSkillsResult builds the /skill <name...> activation summary.
func activateSkillsResult(eng *mow.Engine, requested, activated, unknown []string) slash.Result {
	var b strings.Builder
	if len(activated) > 0 {
		fmt.Fprintf(&b, "Activated %d skill(s) for subsequent turns:\n", len(activated))
		for _, n := range activated {
			fmt.Fprintf(&b, "  • %s\n", n)
		}
	} else {
		b.WriteString("No skills activated.\n")
	}
	if len(unknown) > 0 {
		b.WriteString("\nUnknown (not found in skill dirs):\n")
		for _, n := range unknown {
			fmt.Fprintf(&b, "  • %s\n", n)
		}
		b.WriteString("\nRun `/skill` with no arguments to list available names.")
	}
	title := fmt.Sprintf("skill · %d activated", len(activated))
	return slash.Result{Title: title, Body: b.String()}
}
