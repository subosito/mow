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

// toolEntry tracks a registered tool and whether it came from a BeforeNew
// generation (config-driven MCP/cmdhook/acp/lsp) vs a static init registration.
// Hermetic engines (LoadUserConfig=false) only merge static tools plus tools
// registered during the current BeforeNew call, so a prior host setup cannot
// leak process-global user tools into an embedder's Engine.
type toolEntry struct {
	tool Tool
	gen  int // 0 = static (init / RegisterTool outside BeforeNew); >0 = BeforeNew gen
}

// hookEntry tags a lifecycle hook with BeforeNew generation and optional source
// pack name (e.g. "cmdhook") so packs can replace their hooks without leaking
// prior profile registrations across Engine construction.
type hookEntry[T any] struct {
	fn     T
	gen    int    // 0 = static (outside BeforeNew); >0 = BeforeNew generation
	source string // empty for anonymous; packs use a stable id for ClearHookSource
}

var (
	mu         sync.Mutex
	tools      []toolEntry
	commands   []Command
	beforeNew  []func(configPaths ...string) error
	preTool    []hookEntry[PreToolFunc]
	postTool   []hookEntry[PostToolFunc]
	userPrompt []hookEntry[UserPromptFunc]
	sessStart  []hookEntry[SessionStartFunc]
	preCompact []hookEntry[PreCompactFunc]
	preModel   []hookEntry[PreModelFunc]
	afterTurn  []hookEntry[AfterTurnFunc]
	stop       []hookEntry[StopFunc]

	extInstances map[string]*ExtensionState

	// beforeNewGen increments on each BeforeNew; beforeNewActive is true while
	// hooks run so RegisterTool can tag config-sourced tools.
	beforeNewGen    int
	beforeNewActive bool

	// generationRelease runs when the last Engine tied to a BeforeNew generation
	// closes (see NoteEngineGeneration / ReleaseEngineGeneration).
	generationRelease []func(gen int)
	genEngineRefs     map[int]int
)

func currentRegGen() int {
	if beforeNewActive {
		return beforeNewGen
	}
	return 0
}

func keepHookGen(loadUserConfig bool, gen int) bool {
	return loadUserConfig || gen == 0 || gen == beforeNewGen
}

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

// ClearExtensionKind removes all extension instances of the given kind
// (e.g. "cmdhook") so a pack can re-register for a new BeforeNew generation.
func ClearExtensionKind(kind string) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for k, inst := range extInstances {
		if inst != nil && strings.EqualFold(inst.Kind, kind) {
			delete(extInstances, k)
		}
	}
}

// ClearHookSource removes all lifecycle hooks tagged with source (e.g. "cmdhook").
// Used by packs that re-register on every BeforeNew so prior profile hooks do
// not accumulate in the process-global registry.
func ClearHookSource(source string) {
	source = strings.TrimSpace(source)
	if source == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preTool = filterHookSource(preTool, source)
	postTool = filterHookSource(postTool, source)
	userPrompt = filterHookSource(userPrompt, source)
	sessStart = filterHookSource(sessStart, source)
	preCompact = filterHookSource(preCompact, source)
	preModel = filterHookSource(preModel, source)
	afterTurn = filterHookSource(afterTurn, source)
	stop = filterHookSource(stop, source)
}

func filterHookSource[T any](in []hookEntry[T], source string) []hookEntry[T] {
	if len(in) == 0 {
		return nil
	}
	out := in[:0]
	for _, e := range in {
		if e.source != source {
			out = append(out, e)
		}
	}
	// Avoid retaining dropped entries in the underlying array.
	if len(out) == 0 {
		return nil
	}
	return append([]hookEntry[T](nil), out...)
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
//
// Tools registered while BeforeNew is running are tagged as config-sourced for
// that generation so hermetic Engines can exclude leftovers from a prior host
// setup while still accepting tools from the current BeforeNew (explicit
// ConfigPaths) and static init registrations (proc, contextsink, …).
func RegisterTool(t Tool) {
	if t == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	gen := 0
	if beforeNewActive {
		gen = beforeNewGen
	}
	name := strings.ToLower(strings.TrimSpace(t.Name()))
	for i, ex := range tools {
		if strings.ToLower(strings.TrimSpace(ex.tool.Name())) == name {
			tools[i] = toolEntry{tool: t, gen: gen}
			return
		}
	}
	tools = append(tools, toolEntry{tool: t, gen: gen})
}

// Tools returns a copy of all registered extension tools (every generation).
func Tools() []Tool {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Tool, len(tools))
	for i, e := range tools {
		out[i] = e.tool
	}
	return out
}

// ToolsForEngine returns extension tools appropriate for Engine construction.
// When loadUserConfig is true (CLI/host), every registered tool is included.
// When false (hermetic embedding), only static tools (registered outside
// BeforeNew) and tools registered during the most recent BeforeNew call are
// included — config-driven tools from an earlier host New do not leak in.
func ToolsForEngine(loadUserConfig bool) []Tool {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Tool, 0, len(tools))
	for _, e := range tools {
		if loadUserConfig || e.gen == 0 || e.gen == beforeNewGen {
			out = append(out, e.tool)
		}
	}
	return out
}

// RegisterPreTool appends a global pre-tool hook (deny / rewrite args / extra context).
func RegisterPreTool(fn PreToolFunc) { RegisterPreToolSource("", fn) }

// RegisterPreToolSource is RegisterPreTool with a pack source id for ClearHookSource.
func RegisterPreToolSource(source string, fn PreToolFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preTool = append(preTool, hookEntry[PreToolFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterPostTool appends a global post-tool hook (rewrite result shown to model).
func RegisterPostTool(fn PostToolFunc) { RegisterPostToolSource("", fn) }

// RegisterPostToolSource is RegisterPostTool with a pack source id.
func RegisterPostToolSource(source string, fn PostToolFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	postTool = append(postTool, hookEntry[PostToolFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterUserPrompt appends a global user-prompt hook (rewrite text / system append).
func RegisterUserPrompt(fn UserPromptFunc) { RegisterUserPromptSource("", fn) }

// RegisterUserPromptSource is RegisterUserPrompt with a pack source id.
func RegisterUserPromptSource(source string, fn UserPromptFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	userPrompt = append(userPrompt, hookEntry[UserPromptFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterSessionStart appends a global session-start hook (system append on Engine New).
func RegisterSessionStart(fn SessionStartFunc) { RegisterSessionStartSource("", fn) }

// RegisterSessionStartSource is RegisterSessionStart with a pack source id.
func RegisterSessionStartSource(source string, fn SessionStartFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	sessStart = append(sessStart, hookEntry[SessionStartFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterPreCompact appends a global pre-compact hook.
func RegisterPreCompact(fn PreCompactFunc) { RegisterPreCompactSource("", fn) }

// RegisterPreCompactSource is RegisterPreCompact with a pack source id.
func RegisterPreCompactSource(source string, fn PreCompactFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preCompact = append(preCompact, hookEntry[PreCompactFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterAfterTurn appends a global after-turn hook.
func RegisterAfterTurn(fn AfterTurnFunc) { RegisterAfterTurnSource("", fn) }

// RegisterAfterTurnSource is RegisterAfterTurn with a pack source id.
func RegisterAfterTurnSource(source string, fn AfterTurnFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	afterTurn = append(afterTurn, hookEntry[AfterTurnFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterStop appends a global stop hook (after Prompt finishes).
func RegisterStop(fn StopFunc) { RegisterStopSource("", fn) }

// RegisterStopSource is RegisterStop with a pack source id.
func RegisterStopSource(source string, fn StopFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	stop = append(stop, hookEntry[StopFunc]{fn: fn, gen: currentRegGen(), source: source})
}

// RegisterPreModel appends a global pre-model hook (spend gate, kill switch).
func RegisterPreModel(fn PreModelFunc) { RegisterPreModelSource("", fn) }

// RegisterPreModelSource is RegisterPreModel with a pack source id.
func RegisterPreModelSource(source string, fn PreModelFunc) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	preModel = append(preModel, hookEntry[PreModelFunc]{fn: fn, gen: currentRegGen(), source: source})
}

func collectHooks[T any](entries []hookEntry[T], loadUserConfig bool, filter bool) []T {
	out := make([]T, 0, len(entries))
	for _, e := range entries {
		if filter && !keepHookGen(loadUserConfig, e.gen) {
			continue
		}
		out = append(out, e.fn)
	}
	return out
}

// PreToolHooks returns every registered pre-tool hook (all generations).
func PreToolHooks() []PreToolFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(preTool, true, false)
}

// PreToolHooksForEngine returns pre-tool hooks for Engine construction.
// Hermetic engines (loadUserConfig=false) only see static hooks and the current
// BeforeNew generation — matching ToolsForEngine.
func PreToolHooksForEngine(loadUserConfig bool) []PreToolFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(preTool, loadUserConfig, true)
}

// PostToolHooks returns every registered post-tool hook.
func PostToolHooks() []PostToolFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(postTool, true, false)
}

// PostToolHooksForEngine is the Engine-scoped filter for post-tool hooks.
func PostToolHooksForEngine(loadUserConfig bool) []PostToolFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(postTool, loadUserConfig, true)
}

// UserPromptHooks returns every registered user-prompt hook.
func UserPromptHooks() []UserPromptFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(userPrompt, true, false)
}

// UserPromptHooksForEngine is the Engine-scoped filter for user-prompt hooks.
func UserPromptHooksForEngine(loadUserConfig bool) []UserPromptFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(userPrompt, loadUserConfig, true)
}

// SessionStartHooks returns every registered session-start hook.
func SessionStartHooks() []SessionStartFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(sessStart, true, false)
}

// SessionStartHooksForEngine is the Engine-scoped filter for session-start hooks.
func SessionStartHooksForEngine(loadUserConfig bool) []SessionStartFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(sessStart, loadUserConfig, true)
}

// PreModelHooks returns every registered pre-model hook.
func PreModelHooks() []PreModelFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(preModel, true, false)
}

// PreModelHooksForEngine is the Engine-scoped filter for pre-model hooks.
func PreModelHooksForEngine(loadUserConfig bool) []PreModelFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(preModel, loadUserConfig, true)
}

// PreCompactHooks returns every registered pre-compact hook.
func PreCompactHooks() []PreCompactFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(preCompact, true, false)
}

// PreCompactHooksForEngine is the Engine-scoped filter for pre-compact hooks.
func PreCompactHooksForEngine(loadUserConfig bool) []PreCompactFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(preCompact, loadUserConfig, true)
}

// AfterTurnHooks returns every registered after-turn hook.
func AfterTurnHooks() []AfterTurnFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(afterTurn, true, false)
}

// AfterTurnHooksForEngine is the Engine-scoped filter for after-turn hooks.
func AfterTurnHooksForEngine(loadUserConfig bool) []AfterTurnFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(afterTurn, loadUserConfig, true)
}

// StopHooks returns every registered stop hook.
func StopHooks() []StopFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(stop, true, false)
}

// StopHooksForEngine is the Engine-scoped filter for stop hooks.
func StopHooksForEngine(loadUserConfig bool) []StopFunc {
	mu.Lock()
	defer mu.Unlock()
	return collectHooks(stop, loadUserConfig, true)
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

// BeforeNewGeneration returns the active config/tool generation after the most
// recent BeforeNew call (0 if none yet). Extensions tag process-global resources
// (MCP transports, …) with this value so Engine.Close can release them.
func BeforeNewGeneration() int {
	mu.Lock()
	defer mu.Unlock()
	return beforeNewGen
}

// RegisterGenerationRelease registers a callback when the last Engine for a
// BeforeNew generation closes. Used by extensions that start subprocesses during
// BeforeNew (MCP stdio servers).
func RegisterGenerationRelease(fn func(gen int)) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	generationRelease = append(generationRelease, fn)
}

// NoteEngineGeneration records that an Engine was constructed after BeforeNew
// for gen. Pair with ReleaseEngineGeneration via Engine.RegisterCleanup.
func NoteEngineGeneration(gen int) {
	if gen <= 0 {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if genEngineRefs == nil {
		genEngineRefs = make(map[int]int)
	}
	genEngineRefs[gen]++
}

// GenerationEngineRefs returns how many open Engines were constructed after
// BeforeNew for gen (0 when none or gen <= 0).
func GenerationEngineRefs(gen int) int {
	if gen <= 0 {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	if genEngineRefs == nil {
		return 0
	}
	return genEngineRefs[gen]
}

// ReleaseEngineGeneration drops one Engine reference for gen and invokes
// generation release hooks when the count reaches zero.
func ReleaseEngineGeneration(gen int) {
	if gen <= 0 {
		return
	}
	mu.Lock()
	if genEngineRefs == nil {
		mu.Unlock()
		return
	}
	n, ok := genEngineRefs[gen]
	if !ok {
		mu.Unlock()
		return
	}
	n--
	if n <= 0 {
		delete(genEngineRefs, gen)
	} else {
		genEngineRefs[gen] = n
	}
	refs := n
	fns := append([]func(int){}, generationRelease...)
	mu.Unlock()
	if refs > 0 {
		return
	}
	for _, fn := range fns {
		fn(gen)
	}
}

// BeforeNew invokes all RegisterBeforeNew hooks (best-effort; first error returned).
// Each call bumps the tool registration generation so ToolsForEngine can
// isolate config-sourced tools per Engine construction.
func BeforeNew(configPaths ...string) error {
	mu.Lock()
	beforeNewGen++
	beforeNewActive = true
	fns := append([]func(configPaths ...string) error(nil), beforeNew...)
	mu.Unlock()
	defer func() {
		mu.Lock()
		beforeNewActive = false
		mu.Unlock()
	}()
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
	beforeNewGen = 0
	beforeNewActive = false
	genEngineRefs = nil
	// generationRelease is init-registered (e.g. MCP transport cleanup) and
	// intentionally survives Reset so tests and hosts do not lose release hooks.
}
