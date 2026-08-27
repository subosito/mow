package acp

import (
	"context"
	"strings"

	"github.com/subosito/mow/slash"
)

// availableCommands is the ACP AvailableCommand list from the slash registry
// linked into this binary. Names include the leading slash.
func availableCommands() []map[string]any {
	cmds := slash.Commands()
	out := make([]map[string]any, 0, len(cmds))
	for _, c := range cmds {
		name := "/" + c.Name
		row := map[string]any{
			"name":        name,
			"description": c.Summary,
			"exclusive":   c.Exclusive,
		}
		if len(c.Aliases) > 0 {
			aliases := make([]string, 0, len(c.Aliases))
			for _, a := range c.Aliases {
				a = strings.TrimSpace(a)
				if a == "" {
					continue
				}
				if !strings.HasPrefix(a, "/") {
					a = "/" + a
				}
				aliases = append(aliases, a)
			}
			if len(aliases) > 0 {
				row["aliases"] = aliases
			}
		}
		if c.Usage != "" {
			row["input"] = map[string]any{"hint": strings.TrimSpace(firstLine(c.Usage))}
		}
		out = append(out, row)
	}
	return out
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func (a *agentServer) advertiseCommands(sessionID string) {
	sid := strings.TrimSpace(sessionID)
	if sid == "" {
		return
	}
	cmds := availableCommands()
	if len(cmds) == 0 {
		return
	}
	a.write(notification{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params: mustJSON(map[string]any{
			"sessionId": sid,
			"update": map[string]any{
				"sessionUpdate":     "available_commands_update",
				"availableCommands": cmds,
			},
		}),
	})
}

// splitSlashPrompt reports whether text is a /command invocation.
func splitSlashPrompt(text string) (name string, args []string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", nil, false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", nil, false
	}
	return fields[0], fields[1:], true
}

// trySlash handles an ACP prompt that is a registered slash command. ok is
// false when the text is not a known command (the model should see it).
func (a *agentServer) trySlash(ctx context.Context, sessionID, text string) (body string, exclusiveBusy bool, ok bool) {
	name, args, isSlash := splitSlashPrompt(text)
	if !isSlash {
		return "", false, false
	}
	cmd, found := slash.Lookup(name)
	if !found {
		return "", false, false
	}
	if slash.IsHelpArgs(args) {
		out := strings.TrimSpace(cmd.Usage)
		if out == "" {
			out = cmd.Summary
		}
		return out, false, true
	}
	if cmd.Exclusive && a.eng != nil && a.eng.Status().Busy {
		return "", true, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := cmd.Run(ctx, slash.Request{
		Name:      cmd.Name,
		Invoked:   strings.TrimPrefix(name, "/"),
		Args:      args,
		Engine:    a.eng,
		Workspace: a.eng.Workspace(),
		Color:     false,
	})
	var b strings.Builder
	if t := strings.TrimSpace(res.Title); t != "" {
		b.WriteString(t)
	}
	if body := strings.TrimSpace(res.Body); body != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(body)
	}
	if err != nil {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(err.Error())
	}
	return strings.TrimSpace(b.String()), false, true
}
