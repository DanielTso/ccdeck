package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DanielTso/ccdeck/internal/core"
)

// fakeRunner resolves a fixed set of binaries and answers tmux list-panes
// from a canned reply. It records every Output call.
type fakeRunner struct {
	bins    map[string]string // name to resolved path
	panes   string
	outErr  error
	outputs [][]string // name followed by args of each Output call
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if p, ok := f.bins[file]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: executable file not found in $PATH", file)
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.outputs = append(f.outputs, append([]string{name}, args...))
	return []byte(f.panes), f.outErr
}

// fakeProcs is a process tree: pid to parent pid.
type fakeProcs map[int]int

func (f fakeProcs) Parent(pid int) (int, error) {
	if p, ok := f[pid]; ok {
		return p, nil
	}
	return 0, fmt.Errorf("no process %d", pid)
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

var (
	inTmux = map[string]string{"TMUX": "/tmp/tmux-1000/default,1234,0"}
	noTmux = map[string]string{}
)

func newFakeRunner() *fakeRunner {
	return &fakeRunner{bins: map[string]string{
		"claude": "/usr/local/bin/claude",
		"tmux":   "/usr/bin/tmux",
	}}
}

func TestResumeAndNewBuildCommands(t *testing.T) {
	dir := t.TempDir()
	const claude = "/usr/local/bin/claude"
	const tmux = "/usr/bin/tmux"
	tests := []struct {
		name     string
		mode     Mode
		env      map[string]string
		fork     bool
		new      bool
		wantArgs []string
		wantDir  string
		detached bool
	}{
		{"suspend resume", ModeSuspend, noTmux, false, false,
			[]string{claude, "--resume", "abc-123"}, dir, false},
		{"suspend fork", ModeSuspend, noTmux, true, false,
			[]string{claude, "--resume", "abc-123", "--fork-session"}, dir, false},
		{"suspend new", ModeSuspend, noTmux, false, true,
			[]string{claude}, dir, false},
		{"suspend ignores tmux", ModeSuspend, inTmux, false, false,
			[]string{claude, "--resume", "abc-123"}, dir, false},
		{"auto outside tmux suspends", ModeAuto, noTmux, false, false,
			[]string{claude, "--resume", "abc-123"}, dir, false},
		{"empty mode is auto", "", noTmux, false, false,
			[]string{claude, "--resume", "abc-123"}, dir, false},
		{"auto inside tmux opens a window", ModeAuto, inTmux, false, false,
			[]string{tmux, "new-window", "-c", dir, claude, "--resume", "abc-123"}, "", true},
		{"tmux window fork", ModeTmuxWindow, inTmux, true, false,
			[]string{tmux, "new-window", "-c", dir, claude, "--resume", "abc-123", "--fork-session"}, "", true},
		{"tmux window new", ModeTmuxWindow, inTmux, false, true,
			[]string{tmux, "new-window", "-c", dir, claude}, "", true},
		{"tmux split resume", ModeTmuxSplit, inTmux, false, false,
			[]string{tmux, "split-window", "-c", dir, claude, "--resume", "abc-123"}, "", true},
		{"tmux split new", ModeTmuxSplit, inTmux, false, true,
			[]string{tmux, "split-window", "-c", dir, claude}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeRunner()
			l := New(Options{Mode: tt.mode, Runner: r, Getenv: env(tt.env)})
			var got core.Launch
			var err error
			if tt.new {
				got, err = l.New(dir)
			} else {
				got, err = l.Resume(core.Session{ID: "abc-123", ProjectDir: dir}, tt.fork)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Cmd.Args, tt.wantArgs) {
				t.Errorf("args = %q\nwant   %q", got.Cmd.Args, tt.wantArgs)
			}
			if got.Cmd.Path != tt.wantArgs[0] {
				t.Errorf("path = %q, want %q", got.Cmd.Path, tt.wantArgs[0])
			}
			if got.Cmd.Dir != tt.wantDir {
				t.Errorf("dir = %q, want %q", got.Cmd.Dir, tt.wantDir)
			}
			if got.Detached != tt.detached {
				t.Errorf("detached = %v, want %v", got.Detached, tt.detached)
			}
			if got.Cmd.Process != nil {
				t.Error("the command was started")
			}
			if len(r.outputs) != 0 {
				t.Errorf("building ran %v; only Focus may run a query", r.outputs)
			}
		})
	}
}

func TestCustomClaudeBinary(t *testing.T) {
	dir := t.TempDir()
	r := newFakeRunner()
	r.bins["my-claude"] = "/opt/claude/bin/my-claude"
	l := New(Options{Mode: ModeSuspend, Claude: "my-claude", Runner: r, Getenv: env(noTmux)})
	got, err := l.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmd.Path != "/opt/claude/bin/my-claude" {
		t.Errorf("path = %q", got.Cmd.Path)
	}
}

func TestRelativeClaudePathBecomesAbsolute(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{bins: map[string]string{"./bin/claude": "./bin/claude"}}
	l := New(Options{Mode: ModeSuspend, Claude: "./bin/claude", Runner: r, Getenv: env(noTmux)})
	got, err := l.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.Cmd.Path) {
		t.Errorf("path %q is not absolute; it would resolve against the project directory", got.Cmd.Path)
	}
}

func TestRefusals(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "gone")
	live := &core.Live{PID: 4242, Status: core.StatusIdle}

	tests := []struct {
		name    string
		mode    Mode
		env     map[string]string
		bins    []string // binaries that exist
		session core.Session
		new     string // when set, call New with this dir instead of Resume
		isNew   bool
		want    error
		contain string // substring of the message that helps the user
	}{
		{name: "live session", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ID: "s1", Title: "fix tests", ProjectDir: dir, Live: live},
			want:    ErrLive, contain: "pid 4242"},
		{name: "live beats a missing directory", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ID: "s1", ProjectDir: missing, Live: live}, want: ErrLive},
		{name: "no project dir", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ID: "s1"}, want: ErrNoProjectDir, contain: "no recorded project directory"},
		{name: "project dir gone", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ID: "s1", ProjectDir: missing}, want: ErrNoProjectDir, contain: missing},
		{name: "project dir is a file", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ID: "s1", ProjectDir: file}, want: ErrNoProjectDir, contain: "not a directory"},
		{name: "claude missing", mode: ModeSuspend,
			session: core.Session{ID: "s1", ProjectDir: dir}, want: ErrClaudeNotFound, contain: "[resume] claude"},
		{name: "new: empty dir", mode: ModeSuspend, bins: []string{"claude"}, isNew: true,
			want: ErrNoProjectDir},
		{name: "new: dir gone", mode: ModeSuspend, bins: []string{"claude"}, isNew: true, new: missing,
			want: ErrNoProjectDir},
		{name: "new: claude missing", mode: ModeSuspend, isNew: true, new: dir,
			want: ErrClaudeNotFound},
		{name: "empty session id", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ProjectDir: dir}},
		{name: "session id that looks like a flag", mode: ModeSuspend, bins: []string{"claude"},
			session: core.Session{ID: "--dangerous", ProjectDir: dir}},
		{name: "tmux mode outside tmux", mode: ModeTmuxWindow, env: noTmux, bins: []string{"claude", "tmux"},
			session: core.Session{ID: "s1", ProjectDir: dir}, want: ErrTmuxNotFound, contain: "inside tmux"},
		{name: "tmux binary missing", mode: ModeTmuxSplit, env: inTmux, bins: []string{"claude"},
			session: core.Session{ID: "s1", ProjectDir: dir}, want: ErrTmuxNotFound},
		{name: "auto in tmux without tmux binary", mode: ModeAuto, env: inTmux, bins: []string{"claude"},
			session: core.Session{ID: "s1", ProjectDir: dir}, want: ErrTmuxNotFound},
		{name: "unknown mode", mode: "sideways", bins: []string{"claude"},
			session: core.Session{ID: "s1", ProjectDir: dir}, want: ErrBadMode, contain: "sideways"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{bins: map[string]string{}}
			for _, b := range tt.bins {
				r.bins[b] = "/bin/" + b
			}
			l := New(Options{Mode: tt.mode, Runner: r, Getenv: env(tt.env)})
			var err error
			if tt.isNew {
				_, err = l.New(tt.new)
			} else {
				_, err = l.Resume(tt.session, false)
			}
			if err == nil {
				t.Fatal("want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("error %q is not %v", err, tt.want)
			}
			if tt.contain != "" && !strings.Contains(err.Error(), tt.contain) {
				t.Errorf("error %q does not mention %q", err, tt.contain)
			}
		})
	}
}

func TestFocus(t *testing.T) {
	const tmux = "/usr/bin/tmux"
	// Two panes: a shell (pid 100) that started claude (pid 140) through a
	// wrapper (pid 120), and a second shell (pid 200) with nothing of ours.
	panes := "100 $1 @2 %3\n200 $1 @4 %5\n"
	tree := fakeProcs{140: 120, 120: 100, 100: 90, 90: 1, 200: 90}
	live := func(pid int) core.Session {
		return core.Session{ID: "s1", Live: &core.Live{PID: pid}}
	}
	wantCmd := []string{tmux,
		"switch-client", "-t", "$1", ";",
		"select-window", "-t", "@2", ";",
		"select-pane", "-t", "%3"}

	tests := []struct {
		name    string
		session core.Session
		env     map[string]string
		runner  func(*fakeRunner)
		procs   Procs
		want    []string // nil means ok is false
	}{
		{name: "grandchild of the pane process", session: live(140), env: inTmux, procs: tree, want: wantCmd},
		{name: "the pane process itself", session: live(100), env: inTmux, procs: tree, want: wantCmd},
		{name: "other pane", session: live(200), env: inTmux, procs: tree, want: []string{tmux,
			"switch-client", "-t", "$1", ";", "select-window", "-t", "@4", ";", "select-pane", "-t", "%5"}},
		{name: "innermost pane wins", session: live(140), env: inTmux,
			runner: func(r *fakeRunner) { r.panes = "100 $1 @2 %3\n120 $9 @8 %7\n" },
			procs:  tree, want: []string{tmux,
				"switch-client", "-t", "$9", ";", "select-window", "-t", "@8", ";", "select-pane", "-t", "%7"}},
		{name: "not in tmux", session: live(140), env: noTmux, procs: tree},
		{name: "not live", session: core.Session{ID: "s1"}, env: inTmux, procs: tree},
		{name: "no pid", session: live(0), env: inTmux, procs: tree},
		{name: "no pane owns it", session: live(140), env: inTmux,
			runner: func(r *fakeRunner) { r.panes = "300 $1 @2 %3\n" }, procs: tree},
		{name: "tmux query fails", session: live(140), env: inTmux,
			runner: func(r *fakeRunner) { r.outErr = errors.New("no server running") }, procs: tree},
		{name: "tmux not installed", session: live(140), env: inTmux,
			runner: func(r *fakeRunner) { delete(r.bins, "tmux") }, procs: tree},
		{name: "process vanished", session: live(999), env: inTmux, procs: tree},
		{name: "garbage in the listing", session: live(140), env: inTmux,
			runner: func(r *fakeRunner) { r.panes = "\nnot a pane\nx $1 @2 %3\n100 $1 @2 %3 extra\n" }, procs: tree},
		{name: "process loop", session: live(1), env: inTmux, procs: fakeProcs{1: 2, 2: 3, 3: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newFakeRunner()
			r.panes = panes
			if tt.runner != nil {
				tt.runner(r)
			}
			l := New(Options{Runner: r, Procs: tt.procs, Getenv: env(tt.env)})
			got, ok := l.Focus(tt.session)
			if ok != (tt.want != nil) {
				t.Fatalf("ok = %v, want %v", ok, tt.want != nil)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got.Cmd.Args, tt.want) {
				t.Errorf("args = %q\nwant   %q", got.Cmd.Args, tt.want)
			}
			if !got.Detached {
				t.Error("focus must be detached")
			}
			if got.Cmd.Process != nil {
				t.Error("the command was started")
			}
			wantQuery := [][]string{{tmux, "list-panes", "-a", "-F", paneFormat}}
			if !reflect.DeepEqual(r.outputs, wantQuery) {
				t.Errorf("queries = %q, want %q", r.outputs, wantQuery)
			}
		})
	}
}

func TestFocusStopsAtDepthLimit(t *testing.T) {
	// A chain longer than maxDepth that would reach a pane only at its end.
	tree := fakeProcs{}
	for pid := 1000; pid < 1000+maxDepth+10; pid++ {
		tree[pid] = pid + 1
	}
	r := newFakeRunner()
	r.panes = fmt.Sprintf("%d $1 @2 %%3\n", 1000+maxDepth+5)
	l := New(Options{Runner: r, Procs: tree, Getenv: env(inTmux)})
	if _, ok := l.Focus(core.Session{Live: &core.Live{PID: 1000}}); ok {
		t.Error("walked past the depth limit")
	}
}

func TestParentFromStat(t *testing.T) {
	tests := []struct {
		name    string
		stat    string
		want    int
		wantErr bool
	}{
		{"plain", "1234 (claude) S 77 1234 1234 0 -1 4194560", 77, false},
		{"name with spaces", "1234 (my claude proc) S 88 1 1 0", 88, false},
		{"name with parentheses", "1234 (a (b) c) R 99 1 1 0", 99, false},
		{"name that looks like a stat line", "1234 (x) S 5 1 1 0) S 6 7", 6, false},
		{"name ending in a paren and space", "1234 (evil) ) Z 12 1 1", 12, false},
		{"trailing newline", "1234 (c) S 3\n", 3, false},
		{"no parenthesis", "1234 claude S 77", 0, true},
		{"truncated", "1234 (c) S", 0, true},
		{"nothing after the name", "1234 (c)", 0, true},
		{"ppid not a number", "1234 (c) S x 1", 0, true},
		{"empty", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parentFromStat(tt.stat)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ppid = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestProcFSAndFocusWithAwkwardCommandNames(t *testing.T) {
	root := t.TempDir()
	stat := func(pid int, comm string, ppid int) {
		t.Helper()
		dir := filepath.Join(root, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		line := fmt.Sprintf("%d (%s) S %d %d 0 0 -1 4194560 100 0\n", pid, comm, ppid, pid)
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// pane shell <- "node (worker) 2" <- "claude) S 1 (x"
	stat(500, "bash", 1)
	stat(510, "node (worker) 2", 500)
	stat(520, "claude) S 1 (x", 510)

	p := procFS{root: root}
	if got, err := p.Parent(520); err != nil || got != 510 {
		t.Fatalf("Parent(520) = %d, %v; want 510", got, err)
	}
	if _, err := p.Parent(999); err == nil {
		t.Error("Parent of a missing process should fail")
	}

	r := newFakeRunner()
	r.panes = "500 $1 @2 %3\n"
	l := New(Options{Runner: r, Procs: p, Getenv: env(inTmux)})
	got, ok := l.Focus(core.Session{ID: "s", Live: &core.Live{PID: 520}})
	if !ok {
		t.Fatal("pane not found through awkward command names")
	}
	if got.Cmd.Args[len(got.Cmd.Args)-1] != "%3" {
		t.Errorf("args = %q", got.Cmd.Args)
	}
}
