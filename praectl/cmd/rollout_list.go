package cmd

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"praectl/pkg/client"
)

var (
	allDeviceTypes = []string{"switch", "dpu", "soc", "bmc", "server", "simulator"}
)

var rolloutListCmd = &cobra.Command{
	Use:   "list [deviceType]",
	Short: "List rollout generations",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var typesToQuery []string
		if len(args) == 0 {
			typesToQuery = allDeviceTypes
		} else {
			typesToQuery = []string{strings.ToLower(args[0])}
		}

		c := newClient()
		rollouts := make([]client.Rollout, 0)
		for _, dt := range typesToQuery {
			rs, err := c.ListRollouts(cmd.Context(), dt)
			if err != nil {
				return fmt.Errorf("%s: %w", dt, err)
			}
			rollouts = append(rollouts, rs...)
		}

		if len(rollouts) == 0 {
			fmt.Println("No rollouts found")
			return nil
		}

		sort.SliceStable(rollouts, func(i, j int) bool {
			if rollouts[i].DeviceType == rollouts[j].DeviceType {
				return rollouts[i].Status.Generation > rollouts[j].Status.Generation
			}
			return rollouts[i].DeviceType < rollouts[j].DeviceType
		})

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "TYPE\tGEN\tNAME\tSTATE\tVERSION\tUPDATED\tFAILED\tTARGETS")
		for _, r := range rollouts {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%d\t%d\t%d\n",
				r.DeviceType,
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
