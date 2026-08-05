package mowi

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestLoadConfigWelcomeDefaults(t *testing.T) {
	c := LoadConfig(nil)
	if !c.ShowWelcome() {
		t.Fatal("default welcome on")
	}
	if c.WelcomeText() != DefaultWelcomeMessage {
		t.Fatalf("text=%q", c.WelcomeText())
	}
}

func TestLoadConfigFromYAML(t *testing.T) {
	// Decode extensions.tui payload directly — avoids config.Load / $MOW_HOME.
	c := LoadConfigRaw(func(name string, dst any) error {
		if name != "tui" {
			return nil
		}
		off := false
		*dst.(*Config) = Config{
			Welcome:        &off,
			WelcomeMessage: "hello pack",
			Prompt:         "❯",
		}
		return nil
	})
	if c.ShowWelcome() {
		t.Fatal("welcome should be off")
	}
	if c.WelcomeText() != "hello pack" {
		t.Fatalf("got %q", c.WelcomeText())
	}
	if c.PromptPrefix() != "❯ " {
		t.Fatalf("prompt=%q", c.PromptPrefix())
	}
}

func TestPromptPrefixDefault(t *testing.T) {
	c := Config{}
	if c.PromptPrefix() != "❯ " {
		t.Fatalf("default=%q", c.PromptPrefix())
	}
	c.Prompt = "$"
	if c.PromptPrefix() != "$ " {
		t.Fatalf("override=%q", c.PromptPrefix())
	}
}

func TestKeysDefaultsAndOverrides(t *testing.T) {
	c := LoadConfig(nil)
	if !c.Keys.Matches(c.Keys.Send, "enter") {
		t.Fatal("default send=enter")
	}
	if !c.Keys.Matches(c.Keys.ScrollUp, "ctrl+u") {
		t.Fatal("default scroll_up=ctrl+u")
	}
	// Override + multi-bind
	k := KeysConfig{ScrollUp: "ctrl+u,pgup"}.Resolve()
	if !k.Matches(k.ScrollUp, "ctrl+u") || !k.Matches(k.ScrollUp, "pgup") {
		t.Fatalf("multi scroll_up: %+v", k)
	}
	// Empty fields keep defaults
	if !k.Matches(k.Send, "enter") {
		t.Fatal("send default after partial override")
	}
}

func TestLoadKeysFromYAML(t *testing.T) {
	c := LoadConfigRaw(func(name string, dst any) error {
		if name != "tui" {
			return nil
		}
		*dst.(*Config) = Config{
			Keys: KeysConfig{
				ScrollUp:   "pgup,ctrl+u",
				ScrollDown: "pgdown,ctrl+d",
			},
		}
		return nil
	})
	if !c.Keys.Matches(c.Keys.ScrollUp, "pgup") {
		t.Fatalf("scroll_up=%q", c.Keys.ScrollUp)
	}
	if !c.Keys.Matches(c.Keys.Send, "enter") {
		t.Fatal("send should still default")
	}
}

func TestWelcomeViewCenteredAndDismiss(t *testing.T) {
	raw := newModel(testEngine(t), false, false)
	raw.width, raw.height = 80, 24
	raw.layout()
	raw.ready = true
	if !raw.showWelcome {
		t.Fatal("showWelcome")
	}
	view := raw.View().Content
	if !strings.Contains(view, "mow") {
		t.Fatalf("view=%q", view)
	}
	if !strings.Contains(view, "esc dismiss") {
		t.Fatalf("expected dismiss hint: %q", view)
	}
	mod, _ := raw.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m := mod.(*model)
	if m.showWelcome {
		t.Fatal("esc should dismiss welcome")
	}
}

func TestThemeMonokaiFromYAML(t *testing.T) {
	c := LoadConfigRaw(func(name string, dst any) error {
		if name != "tui" {
			return nil
		}
		*dst.(*Config) = Config{
			Theme: ThemeConfig{
				Name:   "monokai",
				Colors: map[string]string{"accent": "#FFD866"},
			},
		}
		return nil
	})
	if NormalizeThemeName(c.Theme.Name) != "monokai" {
		t.Fatalf("name=%q", c.Theme.Name)
	}
	th := newThemeFrom(c.Theme, true)
	if th.name != "monokai" || !th.mdDark || th.chromaStyle != "" {
		t.Fatalf("theme=%+v", th)
	}
	if th.Accent.GetForeground() == nil {
		t.Fatal("accent fg set")
	}
}

func TestWelcomeDisabledByConfig(t *testing.T) {
	off := false
	c := LoadConfigRaw(func(name string, dst any) error {
		if name != "tui" {
			return nil
		}
		*dst.(*Config) = Config{Welcome: &off}
		return nil
	})
	if c.ShowWelcome() {
		t.Fatal("yaml welcome: false should disable splash")
	}
}
