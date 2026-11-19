package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// versionString reports the build as "version (commit)", or just the version
// when no commit is known. Release builds stamp both through -ldflags; a plain
// go install does not, so the build information Go embeds in every binary
// fills in whatever the flags left unset.
func versionString() string {
	v, c := version, commit
	if info, ok := debug.ReadBuildInfo(); ok {
		v, c = fromBuildInfo(v, c, info)
	}
	if c == "none" {
		return v
	}
	return v + " (" + c + ")"
}

// fromBuildInfo returns v and c with the module version and the short VCS
// revision (marked -dirty when the tree had uncommitted changes) filled in for
// whichever of them is still the unset default. Values set by ldflags win.
func fromBuildInfo(v, c string, info *debug.BuildInfo) (string, string) {
	if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		v = info.Main.Version
	}
	if c == "none" {
		rev, dirty := "", false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value[:min(len(s.Value), 7)]
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			c = rev
			if dirty {
				c += "-dirty"
			}
		}
	}
	return v, c
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ccdeck version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "ccdeck %s\n", versionString())
			return err
		},
	}
}
