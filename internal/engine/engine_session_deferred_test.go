package engine

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestDeferSessionWrite(t *testing.T) {
	for _, mode := range []string{"new", "continue missing", "resume", "continue"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("MOW_HOME", t.TempDir())
			var seen []Message
			opt := Options{
				LoadUserConfig: true,
				Workspace:      t.TempDir(),
				Chat: func(_ context.Context, messages []Message, _ []ToolSpec) (Message, error) {
					seen = append([]Message(nil), messages...)
					return Message{Role: "assistant", Content: "reply"}, nil
				},
			}
			var before []byte
			if mode == "resume" || mode == "continue" {
				seed, err := New(opt)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := seed.Prompt(t.Context(), "saved context"); err != nil {
					t.Fatal(err)
				}
				before, err = os.ReadFile(seed.sess.Path())
				if err != nil {
					t.Fatal(err)
				}
				opt.SessionID = seed.SessionID()
				if err := seed.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "continue" || mode == "continue missing" {
				opt.SessionID = ""
				opt.Continue = true
			}
			opt.DeferSessionWrite = true
			eng, err := New(opt)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			checkUnchanged := func() {
				t.Helper()
				got, err := os.ReadFile(eng.sess.Path())
				if before == nil {
					if !os.IsNotExist(err) {
						t.Fatalf("new session persisted: %q, err=%v", got, err)
					}
				} else if err != nil || !bytes.Equal(got, before) {
					t.Fatalf("existing session changed: err=%v", err)
				}
			}
			checkUnchanged()
			if _, err := eng.PromptWith(t.Context(), "ephemeral aside", PromptOpts{Ephemeral: true}); err != nil {
				t.Fatal(err)
			}
			checkUnchanged()
			if before != nil {
				found := false
				for _, m := range seen {
					if m.Content == "saved context" {
						found = true
					}
				}
				if !found {
					t.Fatal("ephemeral resume lost saved context")
				}
			}
			if _, err := eng.Prompt(t.Context(), "durable turn"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(eng.sess.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(got, []byte("durable turn")) || !bytes.Contains(got, []byte(`"type":"runtime"`)) {
				t.Fatal("normal prompt did not persist history and runtime")
			}
			if bytes.Contains(got, []byte("ephemeral aside")) {
				t.Fatal("ephemeral turn leaked into session")
			}
		})
	}
}

func TestUnusedSessionIsNotStored(t *testing.T) {
	t.Setenv("MOW_HOME", t.TempDir())
	eng, err := New(Options{LoadUserConfig: true, Workspace: t.TempDir(), Chat: func(context.Context, []Message, []ToolSpec) (Message, error) {
		return Message{Role: "assistant", Content: "reply"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	path := eng.sess.Path()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unused session created: %v", err)
	}
	if err := eng.SetEffort("low"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("effort choice created unused session: %v", err)
	}
	if _, err := eng.Prompt(t.Context(), "first conversation"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("first conversation")) || !bytes.Contains(data, []byte(`"type":"runtime"`)) {
		t.Fatal("durable prompt did not persist session")
	}
}
