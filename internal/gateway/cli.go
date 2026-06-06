package gateway

import (
	"context"
	"fmt"
	"os"

	"github.com/aasm3535/lobster/internal/agent"
)

// RunOnce executes a single prompt from the command line (`lobster do "..."`) and exits:
// the same agent as the TUI — tools, memory, skills, MCP — but one turn, scriptable.
// The tool timeline and the answer print to stdout; with output piped (no tty) the
// colours and spinner turn off automatically, so it composes with shell pipelines.
func (g *Gateway) RunOnce(ctx context.Context, prompt string) error {
	enableANSIConsole()
	termColor = terminalColorEnabled()

	g.appCtx = ctx
	defer g.mcp.Close()

	// MCP servers connect quietly; a CLI run shouldn't chat about its startup.
	g.dialMCP(ctx, func(name string, n int, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "mcp %s: %v\n", name, err)
		}
	})

	verb, stream, arch := g.terminalSinkFns()
	sink := newTerminalSink(os.Stdout, verb, stream, arch)

	// Same persistent "local" conversation as the TUI, so a CLI one-liner has the full
	// context of (and is remembered by) your terminal chats.
	sess := agent.NewSession(terminalChatID, g.hist, historyBudgetChars)
	self := g.selfInfo()
	systemFn := func() string {
		return composeSystem(g.cfg.System, self, g.prefsLine(terminalChatID), g.promptSections(), g.mem.Notes(terminalChatID))
	}
	ag := agent.New(g.activeProvider(terminalChatID), g.chatTools(terminalChatID), systemFn, g.cfg.MaxSteps)

	_ = g.sessions.Append(terminalChatID, "user", prompt)
	sink.begin()
	ag.Once(ctx, sess, agent.Input{Text: prompt}, sink)
	return nil
}
