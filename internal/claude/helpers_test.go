package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixture locations under testdata/claude, a synthetic Claude directory.
const (
	fixtureDir = "testdata/claude"
	sessionOne = "11111111-1111-4111-8111-111111111111"
	sessionTwo = "22222222-2222-4222-8222-222222222222"
)

func fixturePaths(t *testing.T) Paths {
	t.Helper()
	return resolvePaths(fixtureDir, "", t.TempDir())
}

func transcriptOne() string {
	return filepath.Join(fixtureDir, "projects", "-home-user-demo", sessionOne+".jsonl")
}

func transcriptTwo() string {
	return filepath.Join(fixtureDir, "projects", "-home-user-other", sessionTwo+".jsonl")
}

// at parses an RFC 3339 time or fails the test.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// fold reads the whole file at path into a new Accumulator.
func fold(t *testing.T, path string) *Accumulator {
	t.Helper()
	acc := NewAccumulator()
	if _, err := NewTailer(path, 0).Read(acc); err != nil {
		t.Fatal(err)
	}
	return acc
}

// foldLines folds lines (each without its newline) into a new Accumulator.
func foldLines(lines ...string) *Accumulator {
	acc := NewAccumulator()
	var off int64
	for _, l := range lines {
		b := []byte(l + "\n")
		acc.Line(off, b)
		off += int64(len(b))
	}
	return acc
}

// writeFile writes content to a new file in a temp dir and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// recorder is a Sink that records what it receives.
type recorder struct {
	offsets []int64
	lines   []string
	resets  int
}

func (r *recorder) Line(off int64, line []byte) {
	r.offsets = append(r.offsets, off)
	r.lines = append(r.lines, string(line))
}

func (r *recorder) Reset() {
	r.resets++
	r.offsets, r.lines = nil, nil
}

// fakeProcs is a ProcessChecker over a fixed pid → start-time table.
type fakeProcs map[int]string

func (f fakeProcs) Alive(pid int, procStart string) bool {
	start, ok := f[pid]
	return ok && (procStart == "" || procStart == start)
}

// line builds a JSON line from fragments, for readable inline fixtures.
func line(parts ...string) string { return "{" + strings.Join(parts, ",") + "}" }
