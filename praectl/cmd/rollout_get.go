package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var rolloutGetCmd = &cobra.Command{
	Use:   "get <deviceType> <rolloutName>",
	Short: "Get rollout details",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceType := strings.ToLower(args[0])
		name := args[1]
		c := newClient()
		rollout, err := c.GetRollout(cmd.Context(), deviceType, name)
		if err != nil {
			return err
		}

		fmt.Printf("Name:\t%s\n", rollout.Name)
		fmt.Printf("DeviceType:\t%s\n", rollout.DeviceType)
		fmt.Printf("Generation:\t%d\n", rollout.Status.Generation)
		fmt.Printf("State:\t%s\n", rollout.Status.State)
		fmt.Printf("Version:\t%s\n", rollout.Spec.Version)
		fmt.Printf("Selector:\t%s\n", formatSelector(rollout.Spec.Selector))
		fmt.Printf("Updated:\t%d\n", rollout.Status.Updated)
		fmt.Printf("Failed:\t%d\n", rollout.Status.Failed)
		fmt.Printf("Targets:\t%d\n", rollout.Status.TotalTargets)
		return nil
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutGetCmd)
}
