package tty

import (
	"testing"

	"github.com/subosito/mow/ext"
)

func TestDefaultInteractiveCommand(t *testing.T) {
	cmd, ok := ext.DefaultInteractiveCommand()
	if !ok || cmd.Name != "tty" {
		t.Fatalf("default interactive command = %q, found=%v; want tty", cmd.Name, ok)
	}
}
