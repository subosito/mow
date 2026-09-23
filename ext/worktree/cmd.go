package worktree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/subosito/mow"
	"github.com/subosito/mow/cliutil"
	"github.com/subosito/mow/ext"
	"github.com/subosito/mow/slash"
)

func init() {
	ext.RegisterCommand(ext.Command{Name: "worktree", Summary: "git worktrees for parallel changes", Layer: "ext", Run: runCommand})
	slash.Register(slash.Command{Name: "worktree", Summary: "Git worktrees for parallel changes — list | add | remove", Usage: slashUsage, Run: runSlash})
	ext.RegisterTool(listTool{})
	ext.RegisterTool(addTool{})
	ext.RegisterTool(removeTool{})
}

const cliUsage = `mow worktree — dedicated git worktrees for parallel changes

  mow worktree list [--dir DIR]
  mow worktree add NAME [--base REF] [--dir DIR]
  mow worktree remove NAME [--force] [--dir DIR]
  mow worktree path NAME [--dir DIR]

Worktrees live beside the repo: <repo>-worktrees/NAME on branch wt/NAME.
Start a parallel session in one with:  mow run --workspace "$(mow worktree path NAME)" …
or mowi --worktree NAME.`

const slashUsage = `/worktree — list worktrees of this repo
/worktree add <name> [base] — new worktree on branch wt/<name>
/worktree remove <name> [--force] — delete it and its branch
Open a parallel session there: mowi --worktree <name>`

func runCommand(args []string) int {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, rest := args[0], args[1:]
	if sub == "help" || sub == "-h" || sub == "--help" {
		fmt.Fprintln(os.Stderr, cliUsage)
		return 0
	}
	fs := cliutil.NewFlagSet("worktree " + sub)
	dir := fs.String("dir", ".", "repository directory")
	base := fs.String("base", "", "base ref for add (default: HEAD)")
	force := fs.Bool("force", false, "remove even with uncommitted or unmerged work")
	// Accept flags after the name too: `add fix-auth --base main`.
	var names []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) > 0 {
			names = append(names, rest[0])
			rest = rest[1:]
		}
	}
	ctx := context.Background()
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	switch sub {
	case "list", "ls":
		list, err := List(ctx, *dir)
		if err != nil {
			return fail(err)
		}
		fmt.Println(formatList(list))
		return 0
	case "add", "new":
		info, err := Add(ctx, *dir, name, *base)
		if err != nil {
			return fail(err)
		}
		fmt.Printf("created %s on %s\n%s\n", info.Name, info.Branch, info.Path)
		return 0
	case "remove", "rm":
		if err := Remove(ctx, *dir, name, *force); err != nil {
			return fail(err)
		}
		fmt.Printf("removed %s\n", name)
		return 0
	case "path":
		if err := ValidateName(name); err != nil {
			return fail(err)
		}
		root, err := Root(ctx, *dir)
		if err != nil {
			return fail(err)
		}
		fmt.Println(Path(root, name))
		return 0
	default:
		fmt.Fprintln(os.Stderr, "mow worktree: unknown command", sub)
		fmt.Fprintln(os.Stderr, cliUsage)
		return 2
	}
}

func fail(err error) int {
	// Library errors already carry a "worktree:" prefix.
	fmt.Fprintln(os.Stderr, "mow", err)
	return 1
}

// formatList renders one worktree per line: marker, name, branch, head, path.
func formatList(list []Info) string {
	var b strings.Builder
	for _, w := range list {
		label := w.Name
		switch {
		case w.Main:
			label = "(main)"
		case label == "":
			label = "(other)"
		}
		state := ""
		if w.Dirty {
			state = " *"
		}
		branch := w.Branch
		if branch == "" {
			branch = "(detached)"
		}
		fmt.Fprintf(&b, "%-20s %-24s %-8s %s%s\n", label, branch, w.Head, w.Path, state)
	}
	return strings.TrimRight(b.String(), "\n")
}

func runSlash(ctx context.Context, req slash.Request) (slash.Result, error) {
	if slash.IsHelpArgs(req.Args) {
		return slash.Result{Title: "/worktree · usage", Body: slashUsage}, nil
	}
	dir := req.Workspace
	if dir == "" && req.Engine != nil {
		dir = req.Engine.Workspace()
	}
	args := req.Args
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		list, err := List(ctx, dir)
		if err != nil {
			return slash.Result{}, err
		}
		return slash.Result{Title: fmt.Sprintf("worktrees · %d", len(list)), Body: formatList(list)}, nil
	}
	switch args[0] {
	case "add", "new":
		if len(args) < 2 {
			return slash.Result{}, errors.New("usage: /worktree add <name> [base]")
		}
		base := ""
		if len(args) > 2 {
			base = args[2]
		}
		info, err := Add(ctx, dir, args[1], base)
		if err != nil {
			return slash.Result{}, err
		}
		return slash.Result{
			Title: "worktree · created " + info.Name,
			Body:  fmt.Sprintf("%s on %s\nOpen a parallel session: mowi --worktree %s", info.Path, info.Branch, info.Name),
		}, nil
	case "remove", "rm":
		if len(args) < 2 {
			return slash.Result{}, errors.New("usage: /worktree remove <name> [--force]")
		}
		force := false
		for _, a := range args[2:] {
			force = force || a == "--force" || a == "-f"
		}
		if err := Remove(ctx, dir, args[1], force); err != nil {
			return slash.Result{}, err
		}
		return slash.Result{Title: "worktree · removed " + args[1]}, nil
	default:
		return slash.Result{}, fmt.Errorf("unknown /worktree command %q\n%s", args[0], slashUsage)
	}
}

// Tools. Listing is read-only. Add/remove create and delete directories and
// branches OUTSIDE the workspace jail (the sibling <repo>-worktrees dir), so
// they share the shell trust bar: --allow-shell.

func engineDir(ctx context.Context) (*mow.Engine, string, error) {
	eng := mow.EngineFromContext(ctx)
	if eng == nil {
		return nil, "", errors.New("error: worktree tools need the engine context")
	}
	return eng, eng.Workspace(), nil
}

func requireShell(ctx context.Context) (string, error) {
	eng, dir, err := engineDir(ctx)
	if err != nil {
		return "", err
	}
	if !eng.AllowShell() {
		return "", errors.New("error: worktree_add/remove require --allow-shell (they create and delete git worktrees outside the workspace)")
	}
	return dir, nil
}

type listTool struct{}

func (listTool) Name() string   { return "worktree_list" }
func (listTool) ReadOnly() bool { return true }
func (listTool) Description() string {
	return "List git worktrees of the workspace repository (main checkout first). Managed worktrees live at <repo>-worktrees/<name> on branch wt/<name>."
}
func (listTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (listTool) Exec(ctx context.Context, _ json.RawMessage) (string, error) {
	_, dir, err := engineDir(ctx)
	if err != nil {
		return err.Error(), nil
	}
	list, err := List(ctx, dir)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	return formatList(list), nil
}

type addTool struct{}

func (addTool) Name() string { return "worktree_add" }
func (addTool) Description() string {
	return "Create a dedicated git worktree for parallel changes: <repo>-worktrees/<name> on a new branch wt/<name>. Args: name (a-z, 0-9, '.', '_', '-'), base (optional ref; default HEAD). Returns the path. The worktree is outside this session's workspace; work there from a separate session. Requires --allow-shell."
}
func (addTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"base":{"type":"string"}},"required":["name"]}`)
}
func (addTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	dir, err := requireShell(ctx)
	if err != nil {
		return err.Error(), nil
	}
	var a struct{ Name, Base string }
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	info, err := Add(ctx, dir, strings.TrimSpace(a.Name), a.Base)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	return fmt.Sprintf("created worktree %s at %s on branch %s (head %s)", info.Name, info.Path, info.Branch, info.Head), nil
}

type removeTool struct{}

func (removeTool) Name() string { return "worktree_remove" }
func (removeTool) Description() string {
	return "Remove a managed git worktree and its wt/<name> branch. Refuses when it has uncommitted changes or unmerged commits unless force=true. Args: name, force (optional). Requires --allow-shell."
}
func (removeTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"force":{"type":"boolean"}},"required":["name"]}`)
}
func (removeTool) Exec(ctx context.Context, args json.RawMessage) (string, error) {
	dir, err := requireShell(ctx)
	if err != nil {
		return err.Error(), nil
	}
	var a struct {
		Name  string
		Force bool
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if err := Remove(ctx, dir, strings.TrimSpace(a.Name), a.Force); err != nil {
		return "error: " + err.Error(), nil
	}
	return "removed worktree " + a.Name, nil
}
