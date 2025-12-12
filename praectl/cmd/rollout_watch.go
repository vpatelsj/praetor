package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const pollInterval = 2 * time.Second

var rolloutWatchCmd = &cobra.Command{
	Use:   "watch <generationId>",
	Short: "Watch rollout progress",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		generationID := args[0]
		c := newClient()

		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			default:
			}

			rollout, err := c.GetRollout(cmd.Context(), generationID)
			if err != nil {
				return err
			}

			fmt.Printf(
				"[%s] Generation %s | Version %s | success=%d failure=%d pending=%d | stage=%s\n",
				time.Now().Format(time.RFC3339),
				rollout.GenerationID(),
				rollout.Version,
				rollout.SuccessCount,
				rollout.FailureCount,
				rollout.Pending(),
				strings.ToUpper(rollout.State),
			)

			if rollout.IsTerminal() {
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
