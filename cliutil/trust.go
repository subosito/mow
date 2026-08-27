package cliutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/subosito/mow"
)

// TrustCommand runs the shared trust subcommand for a CLI host and returns an
// os.Exit-style status code. prog is the display name ("mow" or "mowi").
func TrustCommand(prog string, args []string) int {
	prog = strings.TrimSpace(prog)
	if prog == "" {
		prog = "mow"
	}
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintf(os.Stderr, `%s trust — allow project .mow config and skills

  %s trust [path]           trust this workspace (default: .)
  %s trust --list
  %s trust --revoke [path]

`, prog, prog, prog, prog)
		return 0
	}
	fs := NewFlagSet("trust")
	list := fs.Bool("list", false, "show trusted workspaces")
	revoke := fs.Bool("revoke", false, "revoke trust instead of granting it")
	dir := fs.String("workspace", ".", "workspace to trust/revoke")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 && (*dir == "." || *dir == "") {
		*dir = fs.Arg(0)
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		abs = *dir
	}
	if *list {
		for _, ws := range mow.TrustedWorkspaces() {
			fmt.Println(ws)
		}
		return 0
	}
	if *revoke {
		if err := mow.RevokeWorkspaceTrust(*dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s trust: %v\n", prog, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "%s: untrusted %s\n", prog, abs)
		return 0
	}
	if err := mow.TrustWorkspace(*dir); err != nil {
		fmt.Fprintf(os.Stderr, "%s trust: %v\n", prog, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "%s: trusted %s  (project .mow/config.yaml + skills load)\n", prog, abs)
	return 0
}
