package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVersion(t *testing.T) {
	old, oldCommit := version, commit
	t.Cleanup(func() { version, commit = old, oldCommit })
	version, commit = "1.2.3", "abc1234"

	for _, args := range [][]string{{"version"}, {"--version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := run(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if want := "ccdeck 1.2.3 (abc1234)\n"; out != want {
				t.Errorf("output = %q, want %q", out, want)
			}
		})
	}
}

func TestVersionRejectsArgs(t *testing.T) {
	if _, err := run(t, "version", "extra"); err == nil {
		t.Fatal("expected an error for unexpected arguments")
	}
}

func TestPersistentFlagsDeclared(t *testing.T) {
	root := NewRootCmd()
	for _, name := range []string{"claude-dir", "config", "theme", "demo"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("persistent flag --%s is not declared", name)
		}
	}
}
