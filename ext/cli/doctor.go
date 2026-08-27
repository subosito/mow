package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/subosito/mow/internal/doctor"
	"github.com/subosito/mow/internal/permstore"
	"github.com/subosito/mow/slash"
)

func init() {
	slash.Register(slash.Command{
		Name:    "doctor",
		Summary: "Inspect host and workspace without starting MCP or sessions",
		Usage:   "doctor — side-effect-free checks (config, trust, mcp.json, skills, procs)",
		Run: func(_ context.Context, req slash.Request) (slash.Result, error) {
			ws := workspaceOf(req)
			return slash.Result{Title: "doctor", Body: doctor.Format(doctor.Run(ws))}, nil
		},
	})
	slash.Register(slash.Command{
		Name:    "trace",
		Summary: "Write a redacted diagnostic bundle under $MOW_HOME/traces",
		Usage:   "trace — writes a local Markdown snapshot (no secrets)",
		Run: func(_ context.Context, req slash.Request) (slash.Result, error) {
			ws := workspaceOf(req)
			path, err := doctor.Bundle(ws)
			if err != nil {
				return slash.Result{}, err
			}
			return slash.Result{Title: "trace", Body: "wrote " + path}, nil
		},
	})
	slash.Register(slash.Command{
		Name:    "approvals",
		Aliases: []string{"remember"},
		Summary: "List, remember, or revoke durable tool approvals",
		Usage:   "approvals [list]\napprovals remember allow|deny <tool> [args…]\napprovals revoke <id>",
		Run:     runApprovalsSlash,
	})
}

func workspaceOf(req slash.Request) string {
	if req.Engine != nil {
		if ws := strings.TrimSpace(req.Engine.Workspace()); ws != "" {
			return ws
		}
	}
	return strings.TrimSpace(req.Workspace)
}

func doctorCmd(args []string) int {
	bundle := false
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stderr, "mow doctor [--bundle]")
			fmt.Fprintln(os.Stderr, "  --bundle  write a redacted snapshot under $MOW_HOME/traces")
			return 0
		case "--bundle", "-b":
			bundle = true
		}
	}
	if bundle {
		path, err := doctor.Bundle("")
		if err != nil {
			fmt.Fprintln(os.Stderr, "mow doctor:", err)
			return 1
		}
		fmt.Println(path)
		return 0
	}
	fmt.Println(doctor.Format(doctor.Run("")))
	return 0
}

func approvalsCmd(args []string) int {
	text, err := approvals(args, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mow approvals:", err)
		return 1
	}
	fmt.Println(text)
	return 0
}

func runApprovalsSlash(ctx context.Context, req slash.Request) (slash.Result, error) {
	text, err := approvals(req.Args, workspaceOf(req))
	if err != nil {
		return slash.Result{}, err
	}
	return slash.Result{Title: "approvals", Body: text}, nil
}

func approvals(args []string, workspace string) (string, error) {
	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
		args = args[1:]
	}
	switch sub {
	case "list", "ls", "":
		rules, err := permstore.Load(workspace)
		if err != nil {
			return "", err
		}
		return permstore.FormatList(rules), nil
	case "remember", "allow", "deny":
		decision := permstore.Allow
		tool := ""
		rest := args
		if sub == "allow" || sub == "deny" {
			decision = permstore.Decision(sub)
			if len(rest) == 0 {
				return "", fmt.Errorf("usage: approvals %s <tool> [args…]", sub)
			}
			tool = rest[0]
			rest = rest[1:]
		} else {
			if len(rest) < 2 {
				return "", fmt.Errorf("usage: approvals remember allow|deny <tool> [args…]")
			}
			switch strings.ToLower(rest[0]) {
			case "allow":
				decision = permstore.Allow
			case "deny":
				decision = permstore.Deny
			default:
				return "", fmt.Errorf("decision must be allow or deny")
			}
			tool = rest[1]
			rest = rest[2:]
		}
		rule, err := permstore.Remember(workspace, tool, strings.Join(rest, " "), decision)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("remembered %s %s %s (%s)", rule.Decision, rule.Tool, rule.ID, rule.Args), nil
	case "revoke", "rm":
		if len(args) < 1 {
			return "", fmt.Errorf("usage: approvals revoke <id>")
		}
		ok, err := permstore.Revoke(workspace, args[0])
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("unknown id %s", args[0])
		}
		return "revoked " + args[0], nil
	case "help", "-h", "--help":
		return "approvals [list]\napprovals remember allow|deny <tool> [args…]\napprovals revoke <id>", nil
	default:
		return "", fmt.Errorf("unknown approvals command %q", sub)
	}
}
