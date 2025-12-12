package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"praectl/pkg/client"
)

var (
	rolloutVersion      string
	rolloutSelectors    []string
	rolloutMaxFailRatio float64
)

var rolloutCreateCmd = &cobra.Command{
	Use:   "create <deviceType> <rolloutName>",
	Short: "Create a new Praetor rollout",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceType := strings.ToLower(args[0])
		name := args[1]
		labels, err := parseSelectorFlag(rolloutSelectors)
		if err != nil {
			return err
		}

		c := newClient()
		created, err := c.CreateRollout(cmd.Context(), deviceType, name, client.CreateRolloutRequest{
			Version:     rolloutVersion,
			Selector:    labels,
			MaxFailures: rolloutMaxFailRatio,
		})
		if err != nil {
			return err
		}

		fmt.Printf("Rollout:    %s\n", created.Name)
		fmt.Printf("DeviceType: %s\n", created.DeviceType)
		fmt.Printf("Version:    %s\n", created.Spec.Version)
		fmt.Printf("State:      %s\n", created.Status.State)
		fmt.Printf("Selector:   %s\n", formatSelector(created.Spec.Selector))
		fmt.Printf("Generation: %d\n", created.Status.Generation)
		return nil
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutCreateCmd)
	rolloutCreateCmd.Flags().StringVar(&rolloutVersion, "version", "", "Rollout version to deploy")
	rolloutCreateCmd.Flags().StringArrayVar(&rolloutSelectors, "selector", nil, "Label selector in key=value form (repeatable)")
	rolloutCreateCmd.Flags().Float64Var(&rolloutMaxFailRatio, "max-failures", 0.3, "Maximum acceptable failure ratio before pausing the rollout")
	rolloutCreateCmd.MarkFlagRequired("version")
}

func parseSelectorFlag(values []string) (map[string]string, error) {
	result := make(map[string]string)
	for _, pair := range values {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid selector %q, expected key=value", pair)
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key == "" || val == "" {
			return nil, fmt.Errorf("invalid selector %q, key and value must be non-empty", pair)
		}
		result[key] = val
	}
	return result, nil
}
