package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/DanielTso/ccdeck/internal/core"
)

func newListCmd() *cobra.Command {
	var asJSON, liveOnly bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List sessions, most recently active first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sessions, err := loadSessions(cmd)
			if err != nil {
				return err
			}
			if liveOnly {
				live := sessions[:0]
				for _, s := range sessions {
					if s.Live != nil {
						live = append(live, s)
					}
				}
				sessions = live
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), sessions)
			}
			return writeTable(cmd.OutOrStdout(), sessions, time.Now())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the sessions as JSON")
	c.Flags().BoolVar(&liveOnly, "live", false, "list only sessions a running process owns")
	return c
}

// loadSessions opens the store, waits for the first scan and returns every
// session.
func loadSessions(cmd *cobra.Command) ([]core.Session, error) {
	s := settingsOf(cmd)
	cfg, err := loadConfig(s)
	if err != nil {
		return nil, err
	}
	st, err := openStore(s, cfg.Config, nil, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }() // read-only; Close only saves the cache
	if err := waitReady(cmd.Context(), st); err != nil {
		return nil, err
	}
	return st.Sessions(), nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeTable(w io.Writer, sessions []core.Session, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tSTATUS\tACTIVE\tMODEL\tCONTEXT\tTOKENS\tCOST\tPROJECT\tTITLE")
	for _, s := range sessions {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			shortID(s.ID),
			s.Status(),
			age(s.LastActivity, now),
			orDash(s.Model),
			contextFill(s),
			tokens(s.TotalUsage().Total()),
			cost(s.CostUSD, s.CostKind),
			clip(tildePath(s.ProjectDir), 40),
			clip(clean(s.Title), 60),
		)
	}
	return tw.Flush()
}
