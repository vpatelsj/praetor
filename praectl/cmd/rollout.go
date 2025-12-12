package cmd

import "github.com/spf13/cobra"

var rolloutCmd = &cobra.Command{
	Use:   "rollout",
	Short: "Manage Praetor rollouts",
}

func init() {
	rootCmd.AddCommand(rolloutCmd)
}
