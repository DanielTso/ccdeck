package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/meter"
)

// The status-line helper runs inside the user's Claude Code session on
// every refresh, so it must never disturb it: it always exits 0 and always
// prints a line, reads nothing but stdin, starts nothing but the wrapped
// command, and does no work it does not need (no config, no index, no
// theme). Its flags are parsed by hand, because a flag error from cobra
// would exit 1.

const (
	defaultWrapTimeout = 2 * time.Second
	stdinTimeout       = time.Second // stdin that is never closed
	wrapOutputMax      = 64 << 10    // more than this from --wrap is not a status line
	wrapWaitDelay      = 100 * time.Millisecond
	warnPercent        = 80 // a window or context at least this full stands out
)

// fallbackLine is printed when nothing else can be.
const fallbackLine = "ccdeck\n"

const statuslineHelp = `Status-line helper for Claude Code. Claude Code runs it on every status-line
refresh with a JSON document on stdin; it records Claude Code's own context,
cost and rate-limit figures for the dashboard and prints one status line.

Enable it in Claude Code's settings.json (ccdeck never edits that file):

  { "statusLine": { "type": "command", "command": "ccdeck statusline", "refreshInterval": 30 } }

What it records goes to $XDG_STATE_HOME/ccdeck/meter/<session id>.json
(default ~/.local/state/ccdeck/meter): numbers only, never a prompt, title,
path or model name.

Usage:
  ccdeck statusline [--wrap '<command>'] [--wrap-timeout <duration>]

Flags:
  --wrap '<command>'         run your existing status-line command (with sh -c and
                             the same stdin) and print its output instead of
                             ccdeck's own line; ccdeck's line is printed when it
                             fails, prints nothing or runs too long
  --wrap-timeout <duration>  how long the wrapped command may run (default 2s)

It always exits 0 and always prints a line, whatever the input.`

type statuslineOptions struct {
	wrap        string
	wrapTimeout time.Duration
	help        bool
}

// parseStatuslineArgs reads the flags. It never fails: unknown arguments
// and bad values are ignored.
func parseStatuslineArgs(args []string) statuslineOptions {
	o := statuslineOptions{wrapTimeout: defaultWrapTimeout}
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(args[i], "=")
		next := func() string {
			if hasValue {
				return value
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch name {
		case "--wrap":
			o.wrap = next()
		case "--wrap-timeout":
			if d, err := time.ParseDuration(next()); err == nil && d > 0 {
				o.wrapTimeout = d
			}
		case "-h", "--help":
			o.help = true
		}
	}
	return o
}

func newStatuslineCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "statusline [--wrap '<command>'] [--wrap-timeout <duration>]",
		Short:              "Status-line helper for Claude Code: records its figures for the dashboard",
		Long:               statuslineHelp,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		Run: func(cmd *cobra.Command, args []string) {
			out := cmd.OutOrStdout()
			opts := parseStatuslineArgs(args)
			if opts.help {
				_, _ = fmt.Fprintln(out, statuslineHelp)
				return
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			_, _ = io.WriteString(out, statusline(ctx, opts, cmd.InOrStdin(), cmd.ErrOrStderr(), os.Getenv, time.Now()))
		},
	}
}

// statusline does one run and returns what to print. It never panics out:
// a panic prints the fallback line.
func statusline(ctx context.Context, opts statuslineOptions, stdin io.Reader, stderr io.Writer, getenv func(string) string, now time.Time) (line string) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(stderr, "ccdeck statusline: internal error: %v\n", r)
			line = fallbackLine
		}
	}()
	data := readInput(stdin, stdinTimeout)
	in, err := meter.Decode(data, now)
	if err == nil && in.SessionID != "" {
		if err := writeReport(in.Report); err != nil {
			_, _ = fmt.Fprintf(stderr, "ccdeck statusline: %v\n", err)
		}
	}
	if opts.wrap != "" {
		if out, ok := runWrapped(ctx, opts.wrap, data, opts.wrapTimeout); ok {
			return out
		}
	}
	return formatLine(in, now, columns(getenv), useColor(getenv)) + "\n"
}

// writeReport records r in the state directory.
func writeReport(r meter.Report) error {
	dir, err := meter.Dir()
	if err != nil {
		return err
	}
	return meter.Write(dir, r)
}

// readInput reads stdin, up to meter.MaxInput bytes. A terminal is not
// read (someone ran the command by hand), and stdin that stays open longer
// than timeout is given up on: the run goes on as if it were empty.
func readInput(r io.Reader, timeout time.Duration) []byte {
	if f, ok := r.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
	}
	done := make(chan []byte, 1)
	go func() {
		var b bytes.Buffer
		_, _ = b.ReadFrom(io.LimitReader(r, meter.MaxInput))
		done <- b.Bytes()
	}()
	select {
	case data := <-done:
		return data
	case <-time.After(timeout):
		return nil
	}
}

// runWrapped runs command with sh -c, stdin as its input and the same
// environment, and returns its output. ok is false when it fails, prints
// nothing, prints too much or outlives timeout; it is then killed with
// everything it started. Its stderr is discarded.
//
// A command that exits successfully may leave a background job running,
// such as a cache refresh. Such a job is left alone, and when it still
// holds stdout, what the command printed up to wrapWaitDelay after it
// exited is its output (Wait then reports exec.ErrWaitDelay).
func runWrapped(ctx context.Context, command string, stdin []byte, timeout time.Duration) (out string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.Stdin = bytes.NewReader(stdin)
	var buf cappedBuffer
	c.Stdout = &buf
	c.WaitDelay = wrapWaitDelay
	ownGroup(c)
	err := c.Run()
	s := buf.b.String()
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) || ctx.Err() != nil || buf.over || strings.TrimSpace(s) == "" {
		if c.Process != nil {
			killGroup(c)
		}
		return "", false
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s, true
}

// cappedBuffer keeps the first wrapOutputMax bytes written to it and notes
// when there were more.
type cappedBuffer struct {
	b    bytes.Buffer
	over bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := wrapOutputMax - c.b.Len(); len(p) > room {
		c.over = true
		c.b.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return c.b.Write(p)
}

// columns is the width Claude Code gives the status line; 0 for no limit.
func columns(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv("COLUMNS")))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// useColor reports whether the line may carry colour. Claude Code captures
// the output, so stdout is never a terminal; it renders ANSI colour, so
// colour is used unless NO_COLOR is set or the terminal is dumb.
func useColor(getenv func(string) string) bool {
	return getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

// linePart is one element of the status line.
type linePart struct {
	text string
	warn bool // at or above warnPercent: shown with "!" and, with colour, in bold red
}

// formatLine builds the status line: model, context fill, cost, and the
// tightest rate-limit window with its reset time, fitted to width cells by
// dropping the reset time, then the cost, then the model.
func formatLine(in meter.Input, now time.Time, width int, color bool) string {
	model := in.ModelName
	if model == "" {
		model = in.ModelID
	}
	var ctxPart, costPart, limitPart, resetPart *linePart
	if m := in.Meter; m.HasContext {
		pct := math.Floor(m.ContextPercent)
		ctxPart = &linePart{text: fmt.Sprintf("ctx %.0f%%", pct), warn: pct >= warnPercent}
		if ctxPart.warn {
			ctxPart.text += "!"
		}
	}
	if in.HasCost {
		costPart = &linePart{text: fmt.Sprintf("$%.2f", in.Meter.CostUSD)}
	}
	if l, ok := meter.Tightest(in.Limits, now); ok {
		pct := math.Floor(l.UsedPercent)
		limitPart = &linePart{text: fmt.Sprintf("%s %.0f%%", l.Window, pct), warn: pct >= warnPercent}
		if limitPart.warn {
			limitPart.text += "!"
		}
		if !l.ResetsAt.IsZero() {
			resetPart = &linePart{text: "resets in " + until(now, l.ResetsAt)}
		}
	}
	var modelPart *linePart
	if model != "" {
		modelPart = &linePart{text: model}
	}

	for _, drop := range []**linePart{nil, &resetPart, &costPart, &modelPart} {
		if drop != nil {
			*drop = nil
		}
		parts := []*linePart{modelPart, ctxPart, costPart, limitPart}
		plain, styled := joinParts(parts, resetPart, color)
		if plain == "" {
			return "ccdeck"
		}
		if width <= 0 || ansi.StringWidth(plain) <= width {
			return styled
		}
	}
	plain, _ := joinParts([]*linePart{ctxPart, limitPart}, nil, false)
	return ansi.Truncate(plain, width, "…")
}

// joinParts joins the parts that are present with " · ", the reset time
// following the limit with a space; plain has no styling.
func joinParts(parts []*linePart, reset *linePart, color bool) (plain, styled string) {
	var p, s []string
	for i, x := range parts {
		if x == nil {
			continue
		}
		text, st := x.text, x.text
		if color && x.warn {
			st = "\x1b[1;31m" + x.text + "\x1b[0m"
		}
		if i == len(parts)-1 && reset != nil {
			text += " " + reset.text
			st += " " + reset.text
		}
		p, s = append(p, text), append(s, st)
	}
	return strings.Join(p, " · "), strings.Join(s, " · ")
}

// until is a compact duration to t: "<1m", "45m", "2h10m", "3d4h".
func until(now, t time.Time) string {
	d := t.Sub(now)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
}
