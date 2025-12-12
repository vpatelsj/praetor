package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const pollInterval = 2 * time.Second

var rolloutWatchCmd = &cobra.Command{
	Use:   "watch <deviceType> <rolloutName>",
	Short: "Watch rollout progress",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceType := strings.ToLower(args[0])
		name := args[1]
		c := newClient()

		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			default:
			}

			rollout, err := c.GetRollout(cmd.Context(), deviceType, name)
			if err != nil {
				return err
			}

			fmt.Printf(
				"[%s] Generation %d | Version %s | updated=%d failed=%d targets=%d | state=%s\n",
				time.Now().Format(time.RFC3339),
				rollout.Status.Generation,
				rollout.Spec.Version,
				rollout.Status.Updated,
				rollout.Status.Failed,
				rollout.Status.TotalTargets,
				strings.ToUpper(rollout.Status.State),
			)

			if isTerminalState(rollout.Status.State) {
				return nil
			}

			select {
			case <-ticker.C:
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			}
		}
	},
}

func init() {
	rolloutCmd.AddCommand(rolloutWatchCmd)
}

func isTerminalState(state string) bool {
	switch strings.ToLower(state) {
	case "succeeded", "failed", "paused":
		return true
	default:
		return false
	}
}
