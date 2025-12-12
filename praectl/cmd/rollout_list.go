package cmd

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var rolloutListCmd = &cobra.Command{
	Use:   "list <deviceType>",
	Short: "List rollout generations",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceType := strings.ToLower(args[0])
		c := newClient()
		rollouts, err := c.ListRollouts(cmd.Context(), deviceType)
		if err != nil {
			return err
		}

		if len(rollouts) == 0 {
			fmt.Println("No rollouts found")
			return nil
		}

		sort.SliceStable(rollouts, func(i, j int) bool {
			return rollouts[i].Status.Generation > rollouts[j].Status.Generation
		})

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "GEN\tNAME\tSTATE\tVERSION\tUPDATED\tFAILED\tTARGETS")
		for _, r := range rollouts {
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\t%d\n",
				r.Status.Generation,
				r.Name,
				r.Status.State,
				r.Spec.Version,
				r.Status.Updated,
				r.Status.Failed,
				r.Status.TotalTargets,
			)
		}
		return tw.Flush()
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutListCmd)
}
