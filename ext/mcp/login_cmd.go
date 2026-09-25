package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
)

// loginCmd is `mow mcp login <server>`: the one interactive step for an
// `auth: {type: oauth}` server. Engines reuse and refresh the saved grant.
func loginCmd(args []string) int {
	fs := cliutil.NewFlagSet("mcp login")
	config := fs.String("config", "", "config file (default $MOW_HOME/config.yaml)")
	workspace := fs.String("workspace", "", "workspace (default cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mow mcp login <server>")
		return 2
	}
	name := fs.Arg(0)
	s, err := findServer(name, *config, *workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow mcp login: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err = oauthLogin(ctx, s, func(u string) {
		fmt.Fprintf(os.Stderr, "Open this URL to log in to %s:\n\n  %s\n\nWaiting for the browser…\n", s.Name, u)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mow mcp login: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s. Saved to %s\n", s.Name, oauthStorePath(s.Name))
	return 0
}

// logoutCmd is `mow mcp logout <server>`: forget the saved grant.
func logoutCmd(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: mow mcp logout <server>")
		return 2
	}
	path := oauthStorePath(args[0])
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "mow mcp logout: %s was not logged in\n", args[0])
			return 0
		}
		fmt.Fprintf(os.Stderr, "mow mcp logout: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "Logged out of %s\n", args[0])
	return 0
}

// findServer resolves one configured server by name (case-insensitive) the
// same way engine setup does, including the user config.
func findServer(name, config, workspace string) (ServerConfig, error) {
	paths := []string{filepath.Join(mow.Home(), "config.yaml")}
	if c := strings.TrimSpace(config); c != "" {
		paths = append(paths, c)
	}
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws, _ = os.Getwd()
	}
	servers, err := configuredServers(ext.SetupContext{Workspace: ws, LoadUserConfig: true, ConfigPaths: paths})
	if err != nil {
		return ServerConfig{}, err
	}
	var names []string
	for _, s := range servers {
		if strings.EqualFold(s.Name, name) {
			if strings.TrimSpace(s.URL) == "" {
				return ServerConfig{}, fmt.Errorf("%s is a command server; OAuth login is for url servers", s.Name)
			}
			if !strings.EqualFold(strings.TrimSpace(s.Auth.Type), "oauth") {
				return ServerConfig{}, fmt.Errorf("%s does not use `auth: {type: oauth}`", s.Name)
			}
			return s, nil
		}
		names = append(names, s.Name)
	}
	if len(names) == 0 {
		return ServerConfig{}, fmt.Errorf("no MCP servers configured")
	}
	return ServerConfig{}, fmt.Errorf("no MCP server %q (configured: %s)", name, strings.Join(names, ", "))
}

func printLoginUsage(w io.Writer) {
	fmt.Fprint(w, `  mow mcp login <server>    log in to an OAuth MCP server (auth: {type: oauth})
  mow mcp logout <server>   forget its saved login
`)
}
