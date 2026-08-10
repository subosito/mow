// Package ext is the extension registration surface: tools, hooks, CLI commands.
// Feature packs: github.com/subosito/mow/ext/<name> (blank-import to link).
// Helpers (not packs): cliutil (CLI flags), extcfg (decode extensions.*).
// Config: extensions.<name> via Engine.Extension or extcfg.DecodeSection.
package ext

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Tool is a host-executed function (same shape as mow.Tool).
// Packs implement this interface; Engine adapts into the agent loop.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Exec(ctx context.Context, args json.RawMessage) (string, error)
}

// --- Hook function types (mirror mow.*; duplicated to avoid import cycles) ---

// PreToolEvent is emitted before a tool Exec.
type PreToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
}

// PreToolDecision may deny, rewrite args, or attach context for the model.
type PreToolDecision struct {
	Deny              bool
	Message           string
	Args              json.RawMessage
	RewriteArgs       bool
	AdditionalContext string
}

// PreToolFunc runs before each tool call. Returning error aborts the Prompt.
type PreToolFunc func(ctx context.Context, e PreToolEvent) (PreToolDecision, error)

// PostToolEvent is emitted after Exec (or after deny).
type PostToolEvent struct {
	Name       string
	Args       json.RawMessage
	ToolCallID string
	Result     string
	Denied     bool
	ExecErr    error
}

// PostToolDecision may replace the tool result string shown to the model.
type PostToolDecision struct {
	Result  string
	Rewrite bool
}

// PostToolFunc runs after each tool call.
type PostToolFunc func(ctx context.Context, e PostToolEvent) (PostToolDecision, error)

// UserPromptEvent is emitted once at the start of Engine.Prompt.
type UserPromptEvent struct {
	Text      string
	SessionID string
	Workspace string
}

// UserPromptDecision may rewrite the user text or append system instructions.
type UserPromptDecision struct {
	Text         string
	RewriteText  bool
	SystemAppend string
}

// UserPromptFunc runs before the agent loop for a user message.
type UserPromptFunc func(ctx context.Context, e UserPromptEvent) (UserPromptDecision, error)

// SessionStartEvent is emitted once when an Engine is created successfully.
type SessionStartEvent struct {
	Workspace string
	SessionID string
	Model     string
	System    string
}

// SessionStartDecision may append system text for this Engine lifetime.
type SessionStartDecision struct {
	SystemAppend string
}

// SessionStartFunc runs after Engine construction (system/skills already loaded).
type SessionStartFunc func(ctx context.Context, e SessionStartEvent) (SessionStartDecision, error)

// PreCompactEvent is emitted when soft history compaction is about to run.
// MessageCount is len(history). Full messages are available on mow.Options.OnPreCompact
// (avoids duplicating Message types here and import cycles).
type PreCompactEvent struct {
	EstChars     int
	MaxChars     int
	MessageCount int
}

// PreCompactDecision may skip compaction or supply the stub summary text.
// Summary replaces the default compact note (task anchors still applied).
type PreCompactDecision struct {
	Skip    bool
	Summary string
}

// PreModelEvent is emitted immediately before each LLM call — the only seam
// where a policy can refuse to spend. Carries running usage and the size of
// the pending request; deliberately not the history (compaction owns that).
type PreModelEvent struct {
	Turn            int
	InputTokens     int
	OutputTokens    int
	SentChars       int
	CharsPerToken   float64
	MaxOutputTokens int
}

// PreModelDecision may stop the run before the call is made.
type PreModelDecision struct {
	Stop   bool
	Reason string
}

// PreModelFunc runs before each LLM call. First Stop wins; returning an error
// aborts the Run (a spend gate that cannot evaluate must fail closed).
type PreModelFunc func(ctx context.Context, e PreModelEvent) (PreModelDecision, error)

// PreCompactFunc runs before Compact when MaxContextChars is set and history is over budget.
type PreCompactFunc func(ctx context.Context, e PreCompactEvent) (PreCompactDecision, error)

// AfterTurnEvent is emitted after each LLM assistant message is appended.
type AfterTurnEvent struct {
	AssistantText string
	HasToolCalls  bool
}

// AfterTurnFunc runs after each LLM turn (tools may still follow).
type AfterTurnFunc func(ctx context.Context, e AfterTurnEvent)

// StopEvent is emitted when Prompt returns (success or error).
type StopEvent struct {
	Text      string
	Err       error
	SessionID string
}

// StopFunc runs after Prompt finishes (errors ignored).
type StopFunc func(ctx context.Context, e StopEvent)

// Command is a CLI subcommand owned by an extension pack.
type Command struct {
	// Name is the subcommand token (e.g. "acp", "rpc").
	Name string
	// Summary is one-line help text.
	Summary string
	// Run executes the command with remaining args (not including the name).
	// Exit code semantics match os.Exit.
	Run func(args []string) int
	// DefaultInteractive: if true, used when mow is invoked with no args on a TTY.
	// Only one pack should set this; last registration wins.
	DefaultInteractive bool
}

var (
	mu         sync.Mutex
	tools      []Tool
	commands   []Command
	beforeNew  []func(configPaths ...string) error
	preTool    []PreToolFunc
	postTool   []PostToolFunc
	userPrompt []UserPromptFunc
	sessStart  []SessionStartFunc
	preCompact []PreCompactFunc
	preModel   []PreModelFunc
	afterTurn  []AfterTurnFunc
	stop       []StopFunc

	extInstances map[string]*ExtensionState
)

// ExtensionState describes a registered extension instance (e.g. MCP server or cmdhook plugin).
type ExtensionState struct {
	Name     string // Full name, e.g. "cmdhook:context-mode" or "mcp:context-mode"
	Kind     string // "cmdhook" or "mcp"
	MinTurns int    // Activation threshold (0 = always active)
	Enabled  *bool  // Explicit manual toggle override (nil = use MinTurns rule)
}

// ExtensionStatus contains the evaluated state of an extension instance.
type ExtensionStatus struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	MinTurns int    `json:"min_turns"`
	Active   bool   `json:"active"`
	Status   string `json:"status"`
}

type turnKey struct{}

// WithTurn returns a child context carrying the current agent turn number (1-based or 0-based).
func WithTurn(ctx context.Context, turn int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnKey{}, turn)
}

// TurnFromContext extracts the turn number from context (0 if unset).
func TurnFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	v, _ := ctx.Value(turnKey{}).(int)
	return v
}

// RegisterExtensionInstance registers an extension instance (e.g. kind="mcp", name="context-mode").
func RegisterExtensionInstance(kind, name string, minTurns int) {
	if name == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if extInstances == nil {
		extInstances = make(map[string]*ExtensionState)
	}
	full := kind + ":" + name
	if minTurns < 0 {
		minTurns = 0
	}
	extInstances[full] = &ExtensionState{
		Name:     full,
		Kind:     kind,
		MinTurns: minTurns,
	}
}

// SetExtensionEnabled sets explicit enabled/disabled state for extension(s) matching target.
// Target can be full ("mcp:context-mode"), short name ("context-mode"), or kind ("mcp").
func SetExtensionEnabled(target string, enabled bool) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for k, inst := range extInstances {
		short := strings.TrimPrefix(k, inst.Kind+":")
		if strings.EqualFold(k, target) || strings.EqualFold(short, target) || strings.EqualFold(inst.Kind, target) {
			b := enabled
			inst.Enabled = &b
			found = true
		}
	}
	return found
}

// IsExtensionActive reports whether target extension is active at currentTurn.
func IsExtensionActive(target string, currentTurn int) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return true
	}
	mu.Lock()
	defer mu.Unlock()
	for k, inst := range extInstances {
		short := strings.TrimPrefix(k, inst.Kind+":")
		if strings.EqualFold(k, target) || strings.EqualFold(short, target) {
			if inst.Enabled != nil {
				return *inst.Enabled
			}
			if inst.MinTurns <= 0 {
				return true
			}
			return currentTurn >= inst.MinTurns
		}
	}
	// If unmanaged/unregistered, active by default.
	return true
}

// ListExtensions returns evaluated status of all registered extension instances.
func ListExtensions(currentTurn int) []ExtensionStatus {
	mu.Lock()
	defer mu.Unlock()
	var keys []string
	for k := range extInstances {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ExtensionStatus, 0, len(keys))
	for _, k := range keys {
		inst := extInstances[k]
		active := true
		st := "active"
		if inst.Enabled != nil {
			active = *inst.Enabled
			if active {
				st = "active (manual)"
			} else {
				st = "disabled (manual)"
			}
		} else if inst.MinTurns > 0 {
			if currentTurn < inst.MinTurns {
				active = false
				st = fmt.Sprintf("dormant (turn %d/%d)", currentTurn, inst.MinTurns)
			} else {
				st = fmt.Sprintf("active (turn %d/%d)", currentTurn, inst.MinTurns)
			}
		}
		out = append(out, ExtensionStatus{
			Name:     inst.Name,
			Kind:     inst.Kind,
			MinTurns: inst.MinTurns,
			Active:   active,
			Status:   st,
		})
	}
	return out
}

// RegisterTool adds a tool available to integrators and the default registry merge.
// Re-registering a name replaces the earlier tool — BeforeNew may run once per
// Engine, and duplicate specs would otherwise reach the model.
func RegisterTool(t Tool) {
	if t == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(t.Name()))
	for i, ex := range tools {
		if strings.ToLower(strings.TrimSpace(ex.Name())) == name {
			tools[i] = t
			return
		}
	}
	tools = append(tools, t)
}

// Tools returns a copy of registered extension tools.
func Tools() []Tool {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Tool, len(tools))
	copy(out, tools)
	return out
}

// RegisterPreTool appends a global pre-tool hook (deny / rewrite args / extra context).
func RegisterPreTool(fn PreToolFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preTool = append(preTool, fn)
}

// RegisterPostTool appends a global post-tool hook (rewrite result shown to model).
func RegisterPostTool(fn PostToolFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	postTool = append(postTool, fn)
}

// RegisterUserPrompt appends a global user-prompt hook (rewrite text / system append).
func RegisterUserPrompt(fn UserPromptFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	userPrompt = append(userPrompt, fn)
}

// RegisterSessionStart appends a global session-start hook (system append on Engine New).
func RegisterSessionStart(fn SessionStartFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	sessStart = append(sessStart, fn)
}

// RegisterPreCompact appends a global pre-compact hook.
func RegisterPreCompact(fn PreCompactFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preCompact = append(preCompact, fn)
}

// RegisterAfterTurn appends a global after-turn hook.
func RegisterAfterTurn(fn AfterTurnFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	afterTurn = append(afterTurn, fn)
}

// RegisterStop appends a global stop hook (after Prompt finishes).
func RegisterStop(fn StopFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	stop = append(stop, fn)
}

// PreToolHooks returns a copy of registered pre-tool hooks.
func PreToolHooks() []PreToolFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]PreToolFunc(nil), preTool...)
}

// PostToolHooks returns a copy of registered post-tool hooks.
func PostToolHooks() []PostToolFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]PostToolFunc(nil), postTool...)
}

// UserPromptHooks returns a copy of registered user-prompt hooks.
func UserPromptHooks() []UserPromptFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]UserPromptFunc(nil), userPrompt...)
}

// SessionStartHooks returns a copy of registered session-start hooks.
func SessionStartHooks() []SessionStartFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]SessionStartFunc(nil), sessStart...)
}

// RegisterPreModel appends a global pre-model hook (spend gate, kill switch).
func RegisterPreModel(fn PreModelFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preModel = append(preModel, fn)
}

// PreModelHooks returns a copy of registered pre-model hooks.
func PreModelHooks() []PreModelFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]PreModelFunc(nil), preModel...)
}

// PreCompactHooks returns a copy of registered pre-compact hooks.
func PreCompactHooks() []PreCompactFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]PreCompactFunc(nil), preCompact...)
}

// AfterTurnHooks returns a copy of registered after-turn hooks.
func AfterTurnHooks() []AfterTurnFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]AfterTurnFunc(nil), afterTurn...)
}

// StopHooks returns a copy of registered stop hooks.
func StopHooks() []StopFunc {
	mu.Lock()
	defer mu.Unlock()
	return append([]StopFunc(nil), stop...)
}

// RegisterCommand adds a CLI subcommand (typically from pack init).
// Replaces an existing command with the same Name.
func RegisterCommand(c Command) {
	if c.Name == "" || c.Run == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for i, existing := range commands {
		if existing.Name == c.Name {
			commands[i] = c
			return
		}
	}
	commands = append(commands, c)
}

// Commands returns registered subcommands sorted by name.
func Commands() []Command {
	mu.Lock()
	defer mu.Unlock()
	out := append([]Command(nil), commands...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LookupCommand finds a registered subcommand by name.
func LookupCommand(name string) (Command, bool) {
	mu.Lock()
	defer mu.Unlock()
	for _, c := range commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// DefaultInteractiveCommand returns the last registered DefaultInteractive command.
func DefaultInteractiveCommand() (Command, bool) {
	mu.Lock()
	defer mu.Unlock()
	for i := len(commands) - 1; i >= 0; i-- {
		if commands[i].DefaultInteractive {
			return commands[i], true
		}
	}
	return Command{}, false
}

// RegisterBeforeNew runs before mow.New when building engines from CLI packs
// (e.g. acp.RegisterFromConfig so tools exist in the registry).
func RegisterBeforeNew(fn func(configPaths ...string) error) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	beforeNew = append(beforeNew, fn)
}

// BeforeNew invokes all RegisterBeforeNew hooks (best-effort; first error returned).
func BeforeNew(configPaths ...string) error {
	mu.Lock()
	fns := append([]func(configPaths ...string) error(nil), beforeNew...)
	mu.Unlock()
	var first error
	for _, fn := range fns {
		if err := fn(configPaths...); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Reset clears tool, hook, and command registrations (tests only).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	tools = nil
	commands = nil
	beforeNew = nil
	preTool = nil
	postTool = nil
	userPrompt = nil
	sessStart = nil
	preCompact = nil
	preModel = nil
	afterTurn = nil
	stop = nil
	extInstances = nil
}
