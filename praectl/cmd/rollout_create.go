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
	Use:   "create",
	Short: "Create a new Praetor rollout",
	RunE: func(cmd *cobra.Command, args []string) error {
		labels, err := parseSelectorFlag(rolloutSelectors)
		if err != nil {
			return err
		}

		c := newClient()
		created, err := c.CreateRollout(cmd.Context(), client.CreateRolloutRequest{
			Version:         rolloutVersion,
			MatchLabels:     labels,
			MaxFailureRatio: rolloutMaxFailRatio,
		})
		if err != nil {
			return err
		}

		fmt.Printf("Generation: %s\n", created.GenerationID())
		fmt.Printf("Version:    %s\n", created.Version)
		fmt.Printf("State:      %s\n", created.State)
		fmt.Printf("Matches:    %s\n", formatSelector(created.MatchLabels()))
		fmt.Printf("Max Ratio:  %.2f\n", created.MaxFailureRatio)
		fmt.Printf("Targets:    %d\n", created.TotalTargets)
		return nil
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutCreateCmd)
	rolloutCreateCmd.Flags().StringVar(&rolloutVersion, "version", "", "Rollout version to deploy")
	rolloutCreateCmd.Flags().StringArrayVar(&rolloutSelectors, "selector", nil, "Label selector in key=value form (repeatable)")
	rolloutCreateCmd.Flags().Float64Var(&rolloutMaxFailRatio, "max-failure-ratio", 0.3, "Maximum acceptable failure ratio before halting the rollout")
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
