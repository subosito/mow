// Package slash is the registration surface for interactive slash commands.
//
// A slash command is a host-side action a user types into an interactive
// session (`mow tty`, the mowi TUI): it is not a tool the model can call and
// not a CLI subcommand. Packs register here in init, so the command exists in
// every host exactly when the pack is linked — link `packs/review` and
// `/review` and `/sec` appear; drop the blank import and they disappear from
// both hosts at once, with no host-side edit.
//
// The contract is deliberately presentation-free. Run returns text; the host
// decides how to paint it (ANSI on a plain terminal, transcript entries and a
// status chip in a TUI). That split is the whole point: one behavior, two
// presentations, no duplicated flag parsing.
//
// This package may import the public mow API. It is a leaf: the engine must
// never import it, or the cycle returns.
package slash

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/subosito/mow"
)

// Request is one invocation of a slash command.
type Request struct {
	// Name is the command token the user typed, without the leading slash and
	// after alias resolution (a command registered with aliases still sees its
	// canonical Name here — use Invoked for what the user actually typed).
	Name string
	// Invoked is the raw token the user typed, without the leading slash.
	// Commands whose output names itself (`sec · report`) should use this so
	// the echo matches the request.
	Invoked string
	// Args are the whitespace-split arguments after the command token.
	Args []string
	// Engine is the live engine of the interactive session. Commands run
	// against the user's current model and credentials rather than starting a
	// child process, which is the main reason to be a slash command at all.
	Engine *mow.Engine
	// Workspace is the session workspace (Engine.Workspace() when non-nil).
	Workspace string
	// Color reports whether the host renders ANSI itself. A TUI that paints
	// its own theme sets this false so raw escapes do not fight the layout.
	Color bool
}

// Result is the outcome of a slash command, in two parts so a host can choose
// its own density: a one-line status suitable for a chip or a summary line,
// and an optional full body.
type Result struct {
	// Title is a short one-line status ("sec · report · 3 findings").
	Title string
	// Body is the full output. May be empty when Title says everything.
	Body string
}

// Command is an interactive slash command owned by a pack.
type Command struct {
	// Name is the canonical token without the leading slash ("review").
	Name string
	// Aliases are additional tokens that dispatch to this command.
	Aliases []string
	// Summary is one line for /help listings.
	Summary string
	// Usage is the full help text shown for `/<name> help`.
	Usage string
	// Exclusive marks a command that drives the engine and therefore cannot
	// run while a turn is in flight. Hosts refuse it when busy rather than
	// interleaving two conversations on one engine.
	Exclusive bool
	// Run executes the command. Returning an error is a normal outcome (bad
	// flags, empty scope); hosts render it as an error entry, not a crash.
	Run func(ctx context.Context, req Request) (Result, error)
}

var (
	mu       sync.Mutex
	commands []Command
)

// Register adds a slash command. Re-registering a name replaces the earlier
// command: a pack that registers in init must not stack duplicates when its
// package is linked twice through different module paths.
//
// Commands with no Name or no Run are ignored — a half-registered command that
// dispatches to nil would panic in the host, far from the mistake.
func Register(c Command) {
	c.Name = normalize(c.Name)
	if c.Name == "" || c.Run == nil {
		return
	}
	for i, a := range c.Aliases {
		c.Aliases[i] = normalize(a)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, ex := range commands {
		if ex.Name == c.Name {
			commands[i] = c
			return
		}
	}
	commands = append(commands, c)
}

// Commands returns registered commands sorted by name.
func Commands() []Command {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Command, len(commands))
	copy(out, commands)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup resolves a token (with or without the leading slash, any case) to a
// command, following aliases.
func Lookup(token string) (Command, bool) {
	name := normalize(token)
	if name == "" {
		return Command{}, false
	}
	mu.Lock()
	defer mu.Unlock()
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
		for _, a := range c.Aliases {
			if a == name {
				return c, true
			}
		}
	}
	return Command{}, false
}

// Names returns every dispatchable token (names and aliases), sorted. Hosts
// use it for completion menus.
func Names() []string {
	var out []string
	for _, c := range Commands() {
		out = append(out, c.Name)
		out = append(out, c.Aliases...)
	}
	sort.Strings(out)
	return out
}

// HelpLines returns "/name — summary" lines for host help output.
func HelpLines() []string {
	cmds := Commands()
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		line := "/" + c.Name
		if len(c.Aliases) > 0 {
			line += " (/" + strings.Join(c.Aliases, ", /") + ")"
		}
		if c.Summary != "" {
			line += " — " + c.Summary
		}
		out = append(out, line)
	}
	return out
}

// IsHelpArgs reports whether args is an explicit request for a command's
// usage. Only bare help tokens count: `/review --help` is help, but a path
// argument that merely contains "help" is not, so `/review help.go` still
// reviews the file.
func IsHelpArgs(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch strings.TrimSpace(args[0]) {
	case "help", "-h", "--help", "-help", "?":
		return true
	}
	return false
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "/")))
}
