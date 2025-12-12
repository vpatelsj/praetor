package cmd

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var rolloutListCmd = &cobra.Command{
	Use:   "list",
	Short: "List rollout generations",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		rollouts, err := c.ListRollouts(cmd.Context())
		if err != nil {
			return err
		}

		if len(rollouts) == 0 {
			fmt.Println("No rollouts found")
			return nil
		}

		sort.SliceStable(rollouts, func(i, j int) bool {
			return rollouts[i].ID > rollouts[j].ID
		})

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "GENERATION\tVERSION\tSTATE\tSUCCESS\tFAILURE\tPENDING\tSELECTOR")
		for _, r := range rollouts {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
				r.GenerationID(),
				r.Version,
				r.State,
				r.SuccessCount,
				r.FailureCount,
				r.Pending(),
				formatSelector(r.MatchLabels()),
			)
		}
		return tw.Flush()
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutListCmd)
}
