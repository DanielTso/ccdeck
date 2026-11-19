// Package launch implements core.Launcher: it builds the commands that resume,
// start and focus Claude Code sessions. It never runs them; the one exception
// is the read-only tmux query that Focus needs to find a session's pane.
//
// Commands are built as argument vectors and nothing passes through a shell.
// Inside tmux the vector is handed to tmux as separate arguments, which tmux
// 3.0 and later execute directly.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DanielTso/ccdeck/internal/core"
)

// Mode says where a session opens.
type Mode string

// Modes. The values match the [resume] mode setting in the config file.
const (
	ModeAuto       Mode = "auto"        // ModeTmuxWindow inside tmux, ModeSuspend otherwise
	ModeSuspend    Mode = "suspend"     // take over the terminal until claude exits
	ModeTmuxWindow Mode = "tmux-window" // a new tmux window
	ModeTmuxSplit  Mode = "tmux-split"  // split the current tmux window
)

// Errors returned, wrapped with detail, when a launch is refused.
var (
	ErrClaudeNotFound = errors.New("claude binary not found")
	ErrTmuxNotFound   = errors.New("tmux not available")
	ErrNoProjectDir   = errors.New("project directory unavailable")
	ErrLive           = errors.New("session is already running")
	ErrBadMode        = errors.New("unknown resume mode")
)

// configDirEnv is the variable Claude Code reads its directory from.
const configDirEnv = "CLAUDE_CONFIG_DIR"

// focusTimeout bounds the tmux query made by Focus.
const focusTimeout = 2 * time.Second

// maxDepth bounds the walk up a process tree, as a guard against loops.
const maxDepth = 64

// Options configures a Launcher. The zero value is usable: auto mode, claude
// from PATH, the real system.
type Options struct {
	Mode   Mode                // empty means ModeAuto
	Claude string              // binary name or path; empty means "claude"
	Runner Runner              // nil means the real system
	Procs  Procs               // nil means /proc
	Getenv func(string) string // nil means os.Getenv
	// ClaudeDir is the Claude directory ccdeck reads. When set, claude is
	// started with CLAUDE_CONFIG_DIR pointing at it, so it finds the same
	// sessions and account. Leave it empty for the default ~/.claude with
	// CLAUDE_CONFIG_DIR unset.
	ClaudeDir string
	Environ   func() []string // nil means os.Environ
}

// Launcher implements core.Launcher.
type Launcher struct {
	mode   Mode
	claude string
	runner Runner
	procs  Procs
	getenv func(string) string
	// claudeDir is passed to claude as CLAUDE_CONFIG_DIR when not empty.
	claudeDir string
	environ   func() []string
}

var _ core.Launcher = (*Launcher)(nil)

// New returns a Launcher for opts.
func New(opts Options) *Launcher {
	l := &Launcher{
		mode:   opts.Mode,
		claude: opts.Claude,
		runner: opts.Runner,
		procs:  opts.Procs,
		getenv: opts.Getenv,

		claudeDir: opts.ClaudeDir,
		environ:   opts.Environ,
	}
	if l.mode == "" {
		l.mode = ModeAuto
	}
	if l.claude == "" {
		l.claude = "claude"
	}
	if l.runner == nil {
		l.runner = systemRunner{}
	}
	if l.procs == nil {
		l.procs = systemProcs()
	}
	if l.getenv == nil {
		l.getenv = os.Getenv
	}
	if l.environ == nil {
		l.environ = os.Environ
	}
	return l
}

// Resume builds `claude --resume <id>` (with --fork-session when fork is set)
// in the session's project directory. It refuses a session that is running.
func (l *Launcher) Resume(s core.Session, fork bool) (core.Launch, error) {
	if s.Live != nil {
		return core.Launch{}, fmt.Errorf("%w: %s is running as pid %d in %s; focus it instead of resuming it",
			ErrLive, label(s), s.Live.PID, orUnknown(s.ProjectDir))
	}
	if s.ID == "" || strings.HasPrefix(s.ID, "-") {
		return core.Launch{}, fmt.Errorf("cannot resume: %q is not a session id", s.ID)
	}
	args := []string{"--resume", s.ID}
	if fork {
		args = append(args, "--fork-session")
	}
	return l.build(s.ProjectDir, args)
}

// New builds `claude` in dir.
func (l *Launcher) New(dir string) (core.Launch, error) {
	return l.build(dir, nil)
}

// Focus finds the tmux pane whose process tree contains the session's process
// and returns the tmux command that switches to it. ok is false when the
// session is not running, we are not inside tmux, or no pane owns it.
func (l *Launcher) Focus(s core.Session) (core.Launch, bool) {
	if s.Live == nil || s.Live.PID <= 0 || l.getenv("TMUX") == "" {
		return core.Launch{}, false
	}
	tmux, err := l.runner.LookPath("tmux")
	if err != nil {
		return core.Launch{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), focusTimeout)
	defer cancel()
	out, err := l.runner.Output(ctx, tmux, "list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return core.Launch{}, false
	}
	p, ok := l.paneOf(s.Live.PID, parsePanes(string(out)))
	if !ok {
		return core.Launch{}, false
	}
	// A lone ";" argument separates tmux commands, as `\;` does in a shell.
	cmd := exec.Command(tmux,
		"switch-client", "-t", p.session, ";",
		"select-window", "-t", p.window, ";",
		"select-pane", "-t", p.pane)
	return core.Launch{Cmd: cmd, Detached: true}, true
}

// build checks everything a launch needs and returns the command.
func (l *Launcher) build(dir string, claudeArgs []string) (core.Launch, error) {
	if err := checkDir(dir); err != nil {
		return core.Launch{}, err
	}
	claude, err := l.findClaude()
	if err != nil {
		return core.Launch{}, err
	}
	mode, err := l.effectiveMode()
	if err != nil {
		return core.Launch{}, err
	}
	if mode == ModeSuspend {
		cmd := exec.Command(claude, claudeArgs...)
		cmd.Dir = dir
		if l.claudeDir != "" {
			// The child inherits ccdeck's environment, with the Claude
			// directory set last so it wins over any earlier value.
			cmd.Env = append(l.environ(), configDirEnv+"="+l.claudeDir)
		}
		return core.Launch{Cmd: cmd}, nil
	}
	tmux, err := l.runner.LookPath("tmux")
	if err != nil {
		return core.Launch{}, fmt.Errorf("%w: mode %q needs tmux on PATH: %w", ErrTmuxNotFound, mode, err)
	}
	sub := "new-window"
	if mode == ModeTmuxSplit {
		sub = "split-window"
	}
	argv := []string{sub, "-c", dir}
	if l.claudeDir != "" {
		// A tmux window gets the tmux server's environment, not ours, so
		// the Claude directory has to be passed explicitly (tmux 3.0+).
		argv = append(argv, "-e", configDirEnv+"="+l.claudeDir)
	}
	argv = append(argv, claude)
	argv = append(argv, claudeArgs...)
	return core.Launch{Cmd: exec.Command(tmux, argv...), Detached: true}, nil
}

// effectiveMode resolves auto and checks the tmux modes can work here.
func (l *Launcher) effectiveMode() (Mode, error) {
	inTmux := l.getenv("TMUX") != ""
	switch l.mode {
	case ModeAuto:
		if inTmux {
			return ModeTmuxWindow, nil
		}
		return ModeSuspend, nil
	case ModeSuspend:
		return ModeSuspend, nil
	case ModeTmuxWindow, ModeTmuxSplit:
		if !inTmux {
			return "", fmt.Errorf("%w: mode %q only works inside tmux ($TMUX is not set); "+
				"change [resume] mode in the config file", ErrTmuxNotFound, l.mode)
		}
		return l.mode, nil
	default:
		return "", fmt.Errorf("%w %q: use auto, suspend, tmux-window or tmux-split", ErrBadMode, l.mode)
	}
}

// findClaude resolves the claude binary to an absolute path, so the command
// does not depend on the working directory it later runs in.
func (l *Launcher) findClaude() (string, error) {
	path, err := l.runner.LookPath(l.claude)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not on PATH; install Claude Code or set [resume] claude in the config file",
			ErrClaudeNotFound, l.claude)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrClaudeNotFound, l.claude, err)
	}
	return abs, nil
}

func checkDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: the session has no recorded project directory", ErrNoProjectDir)
	}
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		return fmt.Errorf("%w: %s no longer exists or cannot be read (%w)", ErrNoProjectDir, dir, err)
	case !info.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrNoProjectDir, dir)
	}
	return nil
}

// pane is a tmux pane, identified by tmux's own ids.
type pane struct{ session, window, pane string }

// paneFormat makes list-panes print the pane's process id and its tmux ids,
// space separated. The ids look like "$1", "@2" and "%3" and never contain
// spaces.
const paneFormat = "#{pane_pid} #{session_id} #{window_id} #{pane_id}"

// parsePanes reads list-panes output into panes by the pid of their process.
func parsePanes(out string) map[int]pane {
	panes := make(map[int]pane)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		panes[pid] = pane{session: f[1], window: f[2], pane: f[3]}
	}
	return panes
}

// paneOf walks from pid up through its parents and returns the first pane
// whose process it meets.
func (l *Launcher) paneOf(pid int, panes map[int]pane) (pane, bool) {
	for range maxDepth {
		if p, ok := panes[pid]; ok {
			return p, true
		}
		parent, err := l.procs.Parent(pid)
		if err != nil || parent <= 1 || parent == pid {
			return pane{}, false
		}
		pid = parent
	}
	return pane{}, false
}

func label(s core.Session) string {
	if s.Title != "" {
		return fmt.Sprintf("session %q", s.Title)
	}
	return "session " + s.ID
}

func orUnknown(dir string) string {
	if dir == "" {
		return "an unknown directory"
	}
	return dir
}
