package thrash

import (
	"context"

	"github.com/subosito/mow/internal/agent"
)

// InstallForTest wires this pack's guards directly into agent Options.
//
// The normal path is ext.RegisterBeforeNew → the ext hook registry → the
// Engine adapter. Tests that drive agent.Run directly never construct an
// Engine, so nothing would be installed and every guard would silently
// no-op. This binds a fresh state to one Options value.
//
// Exported (not _test.go) so the engine-level e2e can use it too.
func InstallForTest(opt *agent.Options, cfg Config) {
	st := newThrashState(opt.Workspace, cfg)

	opt.Hooks.PreTool = append(opt.Hooks.PreTool, func(ctx context.Context, e agent.PreToolEvent) (agent.PreToolDecision, error) {
		d, err := preTool(st, ctx, e.Name, e.Args, e.ToolCallID)
		return agent.PreToolDecision{Deny: d.Deny, Message: d.Message}, err
	})
	opt.Hooks.PostTool = append(opt.Hooks.PostTool, func(ctx context.Context, e agent.PostToolEvent) (agent.PostToolDecision, error) {
		out, rewrite := postTool(st, e.Name, e.Args, e.ToolCallID, e.Result, e.Denied, e.ExecErr)
		return agent.PostToolDecision{Rewrite: rewrite, Result: out}, nil
	})
	opt.Hooks.AfterTurnDecide = append(opt.Hooks.AfterTurnDecide, func(ctx context.Context, e agent.AfterTurnEvent) (agent.AfterTurnDecision, error) {
		if !st.closeTurn(e.HasToolCalls) {
			return agent.AfterTurnDecision{}, nil
		}
		return agent.AfterTurnDecision{Inject: exploreWarnMessage(st.streak())}, nil
	})
}
