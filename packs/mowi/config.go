package mowi

import (
	"log/slog"
	"strings"

	"github.com/subosito/mow"
)

// Config is the extensions.tui section (YAML only — no MOW_TUI_* env overrides).
//
//	extensions:
//	  tui:
//	    welcome: true
//	    welcome_message: |
//	      custom splash
//	    prompt: "❯"
//	    theme:
//	      name: catppuccin-mocha # default, or any chroma style name
//	      colors:
//	        accent: "#FFD866"   # optional hex overrides
//	    keys:
//	      send: enter
//	      scroll_up: ctrl+u
type Config struct {
	Welcome        *bool  `yaml:"welcome"`
	WelcomeMessage string `yaml:"welcome_message"`
	// Prompt is the input line prefix (default "❯"). Shown as "<prompt> ".
	Prompt string `yaml:"prompt"`
	// Theme selects a named palette and optional color overrides.
	Theme ThemeConfig `yaml:"theme"`
	// Keys are optional overrides; empty fields keep defaults.
	// Multiple bindings: comma-separated, e.g. "ctrl+u,pgup".
	Keys KeysConfig `yaml:"keys"`
}

// KeysConfig is the keyboard map for the TUI. Values are bubbletea key strings
// (msg.String()), comma-separated for aliases.
type KeysConfig struct {
	Send       string `yaml:"send"`
	Newline    string `yaml:"newline"`
	Cancel     string `yaml:"cancel"`
	Quit       string `yaml:"quit"`
	Clear      string `yaml:"clear"`
	Help       string `yaml:"help"`
	PermCycle  string `yaml:"perm_cycle"`
	ScrollUp   string `yaml:"scroll_up"`
	ScrollDown string `yaml:"scroll_down"`
	// Thinking is reserved (thinking is indicator-only; no body toggle).
	Thinking string `yaml:"thinking"`
	// Focus toggles editor vs transcript key focus.
	Focus string `yaml:"focus"`
	// PeerExpand toggles live peer (acp_delegate) text vs a one-line summary.
	PeerExpand string `yaml:"peer_expand"`
	// SelectMode releases mouse tracking so the terminal can drag-select text.
	SelectMode string `yaml:"select_mode"`
	// EffortCycle cycles reasoning effort levels (none/low/medium/high).
	EffortCycle string `yaml:"effort_cycle"`
	// ViewDiff opens the expanded full-screen diff overlay for the latest
	// write/edit card (unified by default; tab toggles split when wide).
	ViewDiff string `yaml:"view_diff"`
}

// DefaultWelcomeMessage is shown when welcome is on and welcome_message is empty.
const DefaultWelcomeMessage = `mowi

type a message to start`

// DefaultPrompt is the input prefix character/string.
const DefaultPrompt = "❯"

// DefaultKeys is the built-in keymap (minimal set).
// Scroll defaults to ctrl+u/d — available on laptop keyboards without PgUp/PgDn.
// Help/focus avoid F-keys (awkward on laptops / repeer-be sessions); ? still opens
// help when the input is empty.
func DefaultKeys() KeysConfig {
	return KeysConfig{
		Send:       "enter",
		Newline:    "ctrl+j",
		Cancel:     "esc",
		Quit:       "ctrl+c",
		Clear:      "ctrl+l",
		Help:       "ctrl+/", // also ? when input is empty
		PermCycle:  "shift+tab",
		ScrollUp:   "ctrl+u",
		ScrollDown: "ctrl+d",
		Thinking:   "ctrl+t", // reserved (indicator-only; no body)
		Focus:      "ctrl+o", // editor ↔ transcript
		PeerExpand: "ctrl+p", // collapsed peer stream ↔ live text
		SelectMode: "ctrl+s", // release mouse so the terminal can select text
		ViewDiff:   "ctrl+e", // expand last write/edit diff to full-screen overlay
	}
}

// Resolve fills empty key fields from defaults.
func (k KeysConfig) Resolve() KeysConfig {
	d := DefaultKeys()
	return KeysConfig{
		Send:       firstNonEmpty(k.Send, d.Send),
		Newline:    firstNonEmpty(k.Newline, d.Newline),
		Cancel:     firstNonEmpty(k.Cancel, d.Cancel),
		Quit:       firstNonEmpty(k.Quit, d.Quit),
		Clear:      firstNonEmpty(k.Clear, d.Clear),
		Help:       firstNonEmpty(k.Help, d.Help),
		PermCycle:  firstNonEmpty(k.PermCycle, d.PermCycle),
		ScrollUp:   firstNonEmpty(k.ScrollUp, d.ScrollUp),
		ScrollDown: firstNonEmpty(k.ScrollDown, d.ScrollDown),
		Thinking:   firstNonEmpty(k.Thinking, d.Thinking),
		Focus:      firstNonEmpty(k.Focus, d.Focus),
		PeerExpand: firstNonEmpty(k.PeerExpand, d.PeerExpand),
		SelectMode: firstNonEmpty(k.SelectMode, d.SelectMode),
		ViewDiff:   firstNonEmpty(k.ViewDiff, d.ViewDiff),
	}
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// Matches reports whether keyStr (tea.KeyPressMsg.String()) is bound in the
// comma-separated field (e.g. "pgup,ctrl+u").
func (k KeysConfig) Matches(field, keyStr string) bool {
	field = strings.TrimSpace(field)
	if field == "" || keyStr == "" {
		return false
	}
	for _, p := range strings.Split(field, ",") {
		if strings.TrimSpace(p) == keyStr {
			return true
		}
	}
	return false
}

// Primary returns the first binding in a comma-separated field (for help text).
func (k KeysConfig) Primary(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return ""
	}
	parts := strings.SplitN(field, ",", 2)
	return strings.TrimSpace(parts[0])
}

// All returns every binding in a field.
func (k KeysConfig) All(field string) []string {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(field, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ShowWelcome reports whether the centered splash should appear.
func (c Config) ShowWelcome() bool {
	if c.Welcome == nil {
		return true
	}
	return *c.Welcome
}

// WelcomeText returns configured or default splash text.
func (c Config) WelcomeText() string {
	if s := strings.TrimSpace(c.WelcomeMessage); s != "" {
		return s
	}
	return DefaultWelcomeMessage
}

// PromptPrefix returns the textarea prompt (always ends with a single space).
func (c Config) PromptPrefix() string {
	p := strings.TrimSpace(c.Prompt)
	if p == "" {
		p = DefaultPrompt
	}
	// Avoid double spaces if user already included one.
	return strings.TrimRight(p, " ") + " "
}

// LoadConfig decodes extensions.tui from the engine (YAML config only).
// A malformed section falls back to defaults with a warning — silently
// ignoring it left users debugging config that was never applied.
func LoadConfig(eng *mow.Engine) Config {
	var c Config
	if eng != nil {
		if err := eng.Extension("tui", &c); err != nil {
			slog.Warn("mowi: extensions.tui ignored (bad yaml)", "err", err)
			c = Config{}
		}
	}
	c.Keys = c.Keys.Resolve()
	return c
}

// LoadConfigRaw decodes from an already-loaded extensions.tui payload (tests).
func LoadConfigRaw(decode func(name string, dst any) error) Config {
	var c Config
	if decode != nil {
		_ = decode("tui", &c)
	}
	c.Keys = c.Keys.Resolve()
	return c
}
