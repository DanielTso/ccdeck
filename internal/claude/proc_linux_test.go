//go:build linux

package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestStatStartTime(t *testing.T) {
	// Field 22 is 962684; the command name contains spaces and parentheses.
	stat := "63709 (my (odd) cmd) S 1 63709 63709 0 -1 4194560 100 0 0 0 5 3 0 0 20 0 12 0 962684 1234 99 18446744073709551615"
	got, ok := statStartTime([]byte(stat))
	if !ok || got != "962684" {
		t.Errorf("start = %q, %v; want 962684", got, ok)
	}
	for _, bad := range []string{"", "no paren", "1 (x) S 1 2"} {
		if _, ok := statStartTime([]byte(bad)); ok {
			t.Errorf("statStartTime(%q) succeeded", bad)
		}
	}
}

func TestProcChecker(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "42"), 0o700); err != nil {
		t.Fatal(err)
	}
	stat := "42 (claude) S 1 42 42 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5000 0 0"
	if err := os.WriteFile(filepath.Join(root, "42", "stat"), []byte(stat), 0o600); err != nil {
		t.Fatal(err)
	}
	pc := procChecker{root: root}
	tests := []struct {
		name      string
		pid       int
		procStart string
		want      bool
	}{
		{"same process", 42, "5000", true},
		{"pid reused", 42, "4999", false},
		{"no start recorded", 42, "", true},
		{"dead pid", 43, "5000", false},
	}
	for _, tt := range tests {
		if got := pc.Alive(tt.pid, tt.procStart); got != tt.want {
			t.Errorf("%s: Alive = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSystemProcessesSeesSelf(t *testing.T) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skip("no /proc")
	}
	start, ok := statStartTime(stat)
	if !ok {
		t.Fatal("cannot parse /proc/self/stat")
	}
	pc := SystemProcesses()
	if !pc.Alive(os.Getpid(), start) {
		t.Error("own process not alive")
	}
	if pc.Alive(os.Getpid(), start+"0") {
		t.Error("own process alive with the wrong start time")
	}
	if pc.Alive(1<<22+7, "") { // above the default pid_max
		t.Error("absurd pid " + strconv.Itoa(1<<22+7) + " alive")
	}
}
