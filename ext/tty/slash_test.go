package tty

import (
	"context"
	"strings"
	"testing"

	"github.com/subosito/mow/slash"
)

// mow tty and host UIs over mow acp must offer the same commands:
// same names, same flags, same behavior — only the presentation differs. That
// property comes from both hosts dispatching through the registry rather than
// each keeping a switch, so these tests assert the dispatch path, not any one pack.

func TestSlashDispatchesRegistered(t *testing.T) {
	var gotArgs []string
	slash.Register(slash.Command{
		Name:    "ttyprobe",
		Summary: "probe",
		Run: func(_ context.Context, req slash.Request) (slash.Result, error) {
			gotArgs = req.Args
			return slash.Result{Title: "probe · done"}, nil
		},
	})

	handled, err := handleSlash(context.Background(), nil, "/ttyprobe --staged ./pkg")
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !handled {
		t.Fatal("registered command not handled")
	}
	// Args must arrive split and without the command token, matching what the
	// pack's flag parser expects.
	if strings.Join(gotArgs, " ") != "--staged ./pkg" {
		t.Errorf("Args = %v, want [--staged ./pkg]", gotArgs)
	}
}

func TestSlashIgnoresUnknown(t *testing.T) {
	// An unregistered token is not a command: it must fall through so the
	// line reaches the model, because "/tmp is full" is a sentence.
	handled, err := handleSlash(context.Background(), nil, "/tmp is full")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handled {
		t.Error("unknown token was treated as a command")
	}
}

func TestSlashPropagatesError(t *testing.T) {
	slash.Register(slash.Command{
		Name: "ttyfail",
		Run: func(context.Context, slash.Request) (slash.Result, error) {
			return slash.Result{}, context.DeadlineExceeded
		},
	})
	handled, err := handleSlash(context.Background(), nil, "/ttyfail")
	if !handled {
		t.Fatal("command not handled")
	}
	// A failing command is still a handled command: reporting handled=false
	// would send the user's slash line to the model as a prompt.
	if err == nil {
		t.Error("error was swallowed")
	}
}

func TestHelpListsRegisteredCommands(t *testing.T) {
	slash.Register(slash.Command{
		Name:    "ttyhelpprobe",
		Summary: "help probe summary",
		Run: func(context.Context, slash.Request) (slash.Result, error) {
			return slash.Result{}, nil
		},
	})
	help := helpText()
	for _, want := range []string{"/model", "/quit", "/ttyhelpprobe", "help probe summary"} {
		if !strings.Contains(help, want) {
			t.Errorf("helpText() missing %q:\n%s", want, help)
		}
	}
}

func TestSlashEmptyLine(t *testing.T) {
	handled, err := handleSlash(context.Background(), nil, "   ")
	if handled || err != nil {
		t.Errorf("blank line: handled=%v err=%v, want false/nil", handled, err)
	}
}
