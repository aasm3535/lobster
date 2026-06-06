package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aasm3535/lobster/internal/event"
)

// terminalSink renders the agent's event stream to stdout for `lobster tui` — the same
// agent the Telegram bot runs. Tool calls show as dim lines, a working spinner ticks while
// the model thinks or a tool runs, and the final reply is rendered from Markdown to ANSI
// (bold, code, lists). Each turn ends by signalling done so the REPL can prompt again.
type terminalSink struct {
	out       io.Writer
	verbosity func() string
	streaming func() bool // kept for signature parity; the terminal renders on completion
	archive   func(role, text string)

	// work, when non-nil, takes over the working indicator: the sink no longer animates
	// its own \r spinner and instead hands the label (or "" to clear) to this callback.
	// The full-screen TUI sets it so the spinner is part of its own managed redraw rather
	// than raw carriage-return writes that would corrupt the layout.
	work func(string)

	done      chan struct{}
	spin      *spinState // animated working indicator, nil when idle
	toolShown bool       // a tool line has been printed this turn (for spacing)
}

// spinState drives the one-line working spinner (a pulsing star with elapsed seconds)
// shown while the model thinks or a tool runs. It lives on its own goroutine so it keeps
// ticking during the blocking model call; Emit stops it before printing anything.
type spinState struct {
	stop chan struct{}
	done chan struct{}
}

// spinFrames is a fixed-width braille spinner. (The old star dingbats ✶✷✸ render at
// ambiguous widths in many terminals, so the trailing text jittered left/right — that's
// the "криво" the loader used to look. Braille cells are reliably one column wide.)
var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (s *terminalSink) spinStart(label string) {
	if s.work != nil {
		s.work(label)
		return
	}
	if !termColor || s.spin != nil {
		return
	}
	sp := &spinState{stop: make(chan struct{}), done: make(chan struct{})}
	s.spin = sp
	go func() {
		defer close(sp.done)
		start := time.Now()
		t := time.NewTicker(110 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-sp.stop:
				return
			case <-t.C:
				suffix := ""
				if el := int(time.Since(start).Seconds()); el >= 1 {
					suffix = fmt.Sprintf(" %ds", el)
				}
				fmt.Fprintf(s.out, "\r  %s %s\x1b[K", tcol(colReply, spinFrames[i%len(spinFrames)]), tdim(label+suffix))
			}
		}
	}()
}

func (s *terminalSink) spinStop() {
	if s.work != nil {
		s.work("")
		return
	}
	if s.spin == nil {
		return
	}
	close(s.spin.stop)
	<-s.spin.done // join before we print, so the spinner never interleaves with output
	fmt.Fprint(s.out, "\r\x1b[K")
	s.spin = nil
}

func newTerminalSink(out io.Writer, verbosity func() string, streaming func() bool, archive func(role, text string)) *terminalSink {
	return &terminalSink{out: out, verbosity: verbosity, streaming: streaming, archive: archive, done: make(chan struct{})}
}

// begin resets per-turn state and arms a fresh done channel; call it right before feeding
// the agent a new message.
func (s *terminalSink) begin() {
	s.done = make(chan struct{})
	s.toolShown = false
}

func (s *terminalSink) finish() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

func (s *terminalSink) level() string {
	if s.verbosity == nil {
		return verbosityNormal
	}
	if v := s.verbosity(); v != "" {
		return v
	}
	return verbosityNormal
}

func (s *terminalSink) Emit(ev event.Event) {
	switch ev.Kind {
	case event.KindThinking:
		s.spinStart("thinking…")

	case event.KindDelta:
		// The terminal renders Markdown on completion, so live token deltas aren't printed
		// (they'd show raw markup and can't be re-flowed). The spinner covers the wait.

	case event.KindToolCall:
		s.spinStop()
		if s.level() != verbosityQuiet {
			if !s.toolShown {
				fmt.Fprintln(s.out) // a blank line sets the tool block apart from the prompt
				s.toolShown = true
			}
			// Green dot + tool name + a short command preview, like the Telegram timeline.
			line := "  " + tcol(colTool, "●") + " " + ev.Tool
			if p := argPreview(ev.Args); p != "" {
				line += tdim("  " + p)
			}
			fmt.Fprintln(s.out, line)
		}
		s.spinStart("running " + ev.Tool + "…") // live indicator while the tool runs

	case event.KindToolResult:
		s.spinStop()
		if s.level() == verbosityVerbose {
			fmt.Fprintln(s.out, tdim("    ↳ "+fmtDur(ev.Elapsed)+"  "+oneLine(ev.Text, 72)))
		}

	case event.KindInterrupt:
		s.spinStop()
		if strings.TrimSpace(ev.Text) != "" {
			fmt.Fprintln(s.out, tdim("  "+ev.Text))
		}

	case event.KindReply:
		s.spinStop()
		if text := strings.TrimSpace(ev.Text); text != "" {
			fmt.Fprintln(s.out) // breathing room above the answer
			s.printReply(mdToANSI(ev.Text))
			if s.archive != nil {
				s.archive("assistant", ev.Text)
			}
		}
		s.finish()

	case event.KindError:
		s.spinStop()
		fmt.Fprintln(s.out)
		fmt.Fprintln(s.out, tcol(colErr, "⚠️  ")+ev.Text)
		s.finish()
	}
}

// argPreview turns a tool call's JSON arguments into a short readable preview — the actual
// command / path / query instead of raw JSON — for the tool line. Falls back to compact
// JSON when no well-known field is present.
func argPreview(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) == nil {
		for _, k := range []string{"command", "cmd", "query", "path", "file_path", "name", "url", "fact", "prompt"} {
			if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
				return oneLine(v, 64)
			}
		}
	}
	return oneLine(raw, 64)
}

// printReply writes the rendered answer: the 🦞 marker leads the first line, and the rest
// is indented to align under it for a clean left margin.
func (s *terminalSink) printReply(body string) {
	for i, line := range strings.Split(body, "\n") {
		switch {
		case i == 0:
			fmt.Fprintln(s.out, tcol(colReply, "🦞  ")+line)
		case line == "":
			fmt.Fprintln(s.out) // don't indent blank lines (no trailing spaces)
		default:
			fmt.Fprintln(s.out, "    "+line)
		}
	}
}
