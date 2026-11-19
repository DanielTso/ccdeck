package launch

import (
	"context"
	"os/exec"
)

// Runner is everything the launcher needs from the operating system's
// process machinery. It is the only place that shells out, so tests supply a
// fake and never run tmux or claude.
type Runner interface {
	// LookPath finds an executable the way exec.LookPath does.
	LookPath(file string) (string, error)
	// Output runs a read-only query and returns its standard output.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type systemRunner struct{}

func (systemRunner) LookPath(file string) (string, error) { return exec.LookPath(file) }

func (systemRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
