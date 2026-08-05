package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/subosito/mow"
	"github.com/subosito/mow/ext/acp"
	"github.com/subosito/mow/extcfg"
)

// PackConfig is extensions.ops — pack-level settings only.
// Fleet catalogs live in named dirs: $MOW_HOME/ops/<name>/config.yaml
type PackConfig struct {
	// Root is the ops profiles directory (default $MOW_HOME/ops).
	Root string `yaml:"root"`
	// LogMaxBytes / LogMaxLines are defaults when a profile omits them.
	LogMaxBytes int `yaml:"log_max_bytes"`
	LogMaxLines int `yaml:"log_max_lines"`
}

// Profile is one named ops unit on disk.
//
//	$ROOT/<name>/
//	  config.yaml   # services, actions, acp peers, optional LLM overrides
//	  prompt.md     # optional system append for this profile
//	  incidents/    # incident JSON store
type Profile struct {
	Name     string
	Dir      string
	Services []Service `yaml:"services"`
	// Log caps override pack defaults when > 0.
	LogMaxBytes int `yaml:"log_max_bytes"`
	LogMaxLines int `yaml:"log_max_lines"`
	// Model / Wire / BaseURL optional LLM overrides applied when MOW_OPS matches
	// this profile (BeforeNew sets MOW_* env so config.Load picks them up).
	// Explicit CLI --model still wins if set after env (Options.Model).
	Model   string `yaml:"model"`
	Wire    string `yaml:"wire"`
	BaseURL string `yaml:"base_url"`
	// Workspace optional default for acp_delegate path jail (parent of peer dirs).
	// Empty = no workspace jail on peers registered from this profile alone.
	Workspace string `yaml:"workspace"`
	// Every is the default interval for `mow ops run` (e.g. "5m"). CLI --every wins.
	Every string `yaml:"every"`
	// RunPrompt is the default user prompt for `mow ops run`. CLI --prompt wins.
	// If empty, a built-in observer prompt is used (prompt.md is still system).
	RunPrompt string `yaml:"prompt"`
	// ACP peers for acp_delegate — self-contained in this profile.
	ACP ProfileACP `yaml:"acp"`
	// Prompt is prompt.md contents (not from yaml).
	Prompt string `yaml:"-"`
}

// ProfileACP mirrors extensions.acp.agents for a self-contained ops unit.
type ProfileACP struct {
	Agents []acp.AgentSpec `yaml:"agents"`
}

// ServiceActions are allowlisted argv lists (no shell). Only these may run.
// Keys are action names (restart, status, …); values are argv arrays. The map
// keeps the on-disk YAML shape unchanged while allowing operators to declare
// additional actions.
type ServiceActions map[string][]string

// Service is one observed process/stack.
type Service struct {
	Name    string         `yaml:"name"`
	Logs    []string       `yaml:"logs"`
	Actions ServiceActions `yaml:"actions"`
	// Health is an optional declared HTTP health probe (ops_health).
	Health *HealthCheck `yaml:"health"`
	// Patterns are declared log regexes with thresholds (ops_log_pattern).
	Patterns []LogPattern `yaml:"patterns"`
	// DependsOn names services this one needs. Declared in one direction
	// only: a reverse `depended_by` would be redundant state that drifts
	// the moment the two disagree, and the reverse edge is derivable.
	// Surfaced in the tick prompt for blast radius, not as a tool — the
	// model can reason about one line of text without a graph API.
	DependsOn []string `yaml:"depends_on"`
	// ACP is the peer name from profile acp.agents for code work (optional).
	ACP   string `yaml:"acp"`
	Notes string `yaml:"notes"`
}

// loadPackConfig reads extensions.ops (optional).
func loadPackConfig(eng *mow.Engine) PackConfig {
	var c PackConfig
	if eng != nil {
		_ = eng.Extension("ops", &c)
	} else {
		_, _ = extcfg.DecodeSection("ops", nil, &c)
	}
	return c
}

func (c PackConfig) root() string {
	if d := strings.TrimSpace(c.Root); d != "" {
		return d
	}
	return filepath.Join(mow.Home(), "ops")
}

func (c PackConfig) logMaxBytes() int {
	if c.LogMaxBytes > 0 {
		return c.LogMaxBytes
	}
	return 256 << 10
}

func (c PackConfig) logMaxLines() int {
	if c.LogMaxLines > 0 {
		return c.LogMaxLines
	}
	return 200
}

// resolveOpsName requires an explicit profile name: tool arg, else MOW_OPS.
// Never auto-picks a single profile.
func resolveOpsName(arg string) (string, error) {
	if s := strings.TrimSpace(arg); s != "" {
		if err := validateOpsName(s); err != nil {
			return "", err
		}
		return s, nil
	}
	if s := strings.TrimSpace(os.Getenv("MOW_OPS")); s != "" {
		if err := validateOpsName(s); err != nil {
			return "", err
		}
		return s, nil
	}
	return "", fmt.Errorf("ops profile name required (tool arg ops=… or env MOW_OPS)")
}

func validateOpsName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid ops name %q", name)
	}
	if strings.Contains(name, "/") || strings.Contains(name, `\`) || strings.Contains(name, "..") {
		return fmt.Errorf("ops name must be a single path segment, got %q", name)
	}
	return nil
}

// loadProfile loads $ROOT/<name>/config.yaml and optional prompt.md.
func loadProfile(name string, pack PackConfig) (Profile, error) {
	if err := validateOpsName(name); err != nil {
		return Profile{}, err
	}
	dir := filepath.Join(pack.root(), name)
	cfgPath := filepath.Join(dir, "config.yaml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Profile{}, fmt.Errorf("ops profile %q not found (expected %s)", name, cfgPath)
		}
		return Profile{}, err
	}
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Profile{}, fmt.Errorf("ops %s: %w", cfgPath, err)
	}
	p.Name = name
	p.Dir = dir
	if pb, err := os.ReadFile(filepath.Join(dir, "prompt.md")); err == nil {
		p.Prompt = strings.TrimSpace(string(pb))
	}
	return p, nil
}

// listProfiles returns names of subdirs under root that contain config.yaml.
func listProfiles(pack PackConfig) ([]string, error) {
	root := pack.root()
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if err := validateOpsName(name); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, name, "config.yaml")); err == nil {
			names = append(names, name)
		}
	}
	return names, nil
}

func (p Profile) logMaxBytes(pack PackConfig) int {
	if p.LogMaxBytes > 0 {
		return p.LogMaxBytes
	}
	return pack.logMaxBytes()
}

func (p Profile) logMaxLines(pack PackConfig) int {
	if p.LogMaxLines > 0 {
		return p.LogMaxLines
	}
	return pack.logMaxLines()
}

func (p Profile) incidentsDir() string {
	return filepath.Join(p.Dir, "incidents")
}

func (p Profile) service(name string) (Service, bool) {
	name = strings.TrimSpace(name)
	for _, s := range p.Services {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return Service{}, false
}

// hasDeps reports whether any service declares a dependency this profile
// actually knows about.
func (p Profile) hasDeps() bool {
	for _, s := range p.Services {
		if len(knownDeps(p, s)) > 0 {
			return true
		}
	}
	return false
}

// knownDeps returns a service's declared dependencies, keeping only names
// that exist in this profile. A typo'd or external dependency would other-
// wise read to the model as a service it can act on, and it cannot.
func knownDeps(p Profile, s Service) []string {
	if len(s.DependsOn) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.DependsOn))
	for _, d := range s.DependsOn {
		d = strings.TrimSpace(d)
		if d == "" || strings.EqualFold(d, s.Name) {
			continue
		}
		if _, ok := p.service(d); ok {
			out = append(out, d)
		}
	}
	return out
}

// actionNames returns the declared action names, sorted for stable output.
func actionNames(actions map[string][]string) []string {
	if len(actions) == 0 {
		return nil
	}
	out := make([]string, 0, len(actions))
	for k, argv := range actions {
		if len(argv) == 0 {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func lookupAction(actions map[string][]string, key string) []string {
	v, _ := actions[key]
	return v
}

func (p Profile) actionArgv(service, action string) ([]string, error) {
	svc, ok := p.service(service)
	if !ok {
		return nil, fmt.Errorf("unknown service %q", service)
	}
	action = strings.ToLower(strings.TrimSpace(action))
	var argv []string
	switch action {
	case "restart":
		argv = lookupAction(svc.Actions, "restart")
	case "status":
		argv = lookupAction(svc.Actions, "status")
	default:
		return nil, fmt.Errorf("action %q not supported (want restart|status)", action)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("service %q has no actions.%s configured", service, action)
	}
	// Reject empty argv elements and bare shell metacharacters in program path.
	for i, a := range argv {
		if strings.TrimSpace(a) == "" {
			return nil, fmt.Errorf("actions.%s has empty argv[%d]", action, i)
		}
	}
	return append([]string(nil), argv...), nil
}

// systemAppend builds SessionStart / status text for a loaded profile.
func (p Profile) systemAppend() string {
	var b strings.Builder
	b.WriteString("Active ops profile: " + p.Name + ". ")
	b.WriteString("Tools: ops_services, ops_logs, ops_action, ops_incident, ops_health, ops_log_pattern, ops_runbook (pass ops=\"" + p.Name + "\" or set MOW_OPS). ")
	if len(p.Services) > 0 {
		b.WriteString("Services: ")
		for i, s := range p.Services {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(s.Name)
			if s.ACP != "" {
				b.WriteString(" [acp:" + s.ACP + "]")
			}
			// List every declared action, not just restart/status: the
			// actions map is operator-defined, and an action the model is
			// never told about may as well not exist.
			if acts := actionNames(s.Actions); len(acts) > 0 {
				b.WriteString(" {" + strings.Join(acts, ",") + "}")
			}
			if deps := knownDeps(p, s); len(deps) > 0 {
				b.WriteString(" [needs:" + strings.Join(deps, ",") + "]")
			}
		}
		b.WriteString(". ")
	}
	if len(p.ACP.Agents) > 0 {
		b.WriteString("ACP peers: ")
		for i, a := range p.ACP.Agents {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(a.Name)
		}
		b.WriteString(". ")
	}
	b.WriteString("Role: continuous monitor + remediate (not log-classify only). ")
	// Advertise runbook names so the model knows guidance exists without
	// having to probe for it; the bodies stay out of the tick until pulled.
	if names, err := listRunbooks(p.runbooksDir()); err == nil && len(names) > 0 {
		if len(names) > maxRunbookList {
			names = names[:maxRunbookList]
		}
		b.WriteString("Runbooks (ops_runbook get name=…): " + strings.Join(names, ", ") + ". ")
	}
	b.WriteString("Prefer ops_logs for evidence; ops_action only for allowlisted restart/status; ")
	b.WriteString("ops_incident for durable work items (open/update/close with stable signatures); ")
	b.WriteString("acp_delegate to a service's acp peer when code or config in that repo can fix the issue. ")
	b.WriteString("Open incidents only for problems that need attention; close when fixed or stale.")
	// Dependencies only pay off if they change behavior, so say what to do
	// with them: correlate rather than ticket each downstream symptom.
	if p.hasDeps() {
		b.WriteString(" [needs:…] marks declared dependencies: when a service and something it needs both look broken, ")
		b.WriteString("treat the dependency as the likely cause — fix it first and record the downstream symptoms on that one incident ")
		b.WriteString("instead of opening a separate incident per affected service.")
	}
	if p.Prompt != "" {
		b.WriteString("\n\n")
		b.WriteString(p.Prompt)
	}
	return b.String()
}

// registerProfileACP merges profile acp.agents into acp_delegate.
func registerProfileACP(p Profile, workspace string) {
	if len(p.ACP.Agents) == 0 {
		return
	}
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws = strings.TrimSpace(p.Workspace)
	}
	acp.AppendAgents(p.ACP.Agents, ws, 0)
}

// applyProfileLLMEnv sets MOW_MODEL / MOW_WIRE / MOW_BASE_URL from the profile
// when MOW_OPS selects it, so the observer Engine uses the profile model.
// Does not override env vars already set by the operator.
func applyProfileLLMEnv(p Profile) {
	if m := strings.TrimSpace(p.Model); m != "" && strings.TrimSpace(os.Getenv("MOW_MODEL")) == "" {
		_ = os.Setenv("MOW_MODEL", m)
	}
	if w := strings.TrimSpace(p.Wire); w != "" && strings.TrimSpace(os.Getenv("MOW_WIRE")) == "" {
		_ = os.Setenv("MOW_WIRE", w)
	}
	if b := strings.TrimSpace(p.BaseURL); b != "" && strings.TrimSpace(os.Getenv("MOW_BASE_URL")) == "" {
		_ = os.Setenv("MOW_BASE_URL", b)
	}
}
