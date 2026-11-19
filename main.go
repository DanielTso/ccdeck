// Command ccdeck is a terminal dashboard for Claude Code sessions.
package main

import (
	"os"

	"github.com/DanielTso/ccdeck/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
