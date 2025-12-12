package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"praectl/pkg/client"
)

var (
	rolloutUpdateVersion      string
	rolloutUpdateSelectors    []string
	rolloutUpdateMaxFailRatio float64
	rolloutUpdateCommand      string
)

var rolloutUpdateCmd = &cobra.Command{
	Use:   "update <deviceType> <rolloutName>",
	Short: "Update a rollout spec and bump its generation",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceType := strings.ToLower(args[0])
		name := args[1]
		labels, err := parseSelectorFlag(rolloutUpdateSelectors)
		if err != nil {
			return err
		}

		var cmdParts []string
		if rolloutUpdateCommand != "" {
			cmdParts = strings.Fields(rolloutUpdateCommand)
		}

		c := newClient()
		updated, err := c.UpdateRollout(cmd.Context(), deviceType, name, client.UpdateRolloutRequest{
			Version:     rolloutUpdateVersion,
			Command:     cmdParts,
			Selector:    labels,
			MaxFailures: rolloutUpdateMaxFailRatio,
		})
		if err != nil {
			return err
		}

		fmt.Printf("Rollout:    %s\n", updated.Name)
		fmt.Printf("DeviceType: %s\n", updated.DeviceType)
		fmt.Printf("Version:    %s\n", updated.Spec.Version)
		fmt.Printf("State:      %s\n", updated.Status.State)
		fmt.Printf("Selector:   %s\n", formatSelector(updated.Spec.Selector))
		fmt.Printf("Generation: %d\n", updated.Status.Generation)
		return nil
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutUpdateCmd)
	rolloutUpdateCmd.Flags().StringVar(&rolloutUpdateVersion, "version", "", "Rollout version to deploy (required)")
	rolloutUpdateCmd.Flags().StringVar(&rolloutUpdateCommand, "command", "", "Command to run during rollout (optional; space-split)")
	rolloutUpdateCmd.Flags().StringArrayVar(&rolloutUpdateSelectors, "selector", nil, "Label selector in key=value form (repeatable)")
	rolloutUpdateCmd.Flags().Float64Var(&rolloutUpdateMaxFailRatio, "max-failures", 0.3, "Maximum acceptable failure ratio before pausing the rollout")
	rolloutUpdateCmd.MarkFlagRequired("version")
}
