package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Display Praetor resources",
}

var getDevicesCmd = &cobra.Command{
	Use:   "devices",
	Short: "List registered devices",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		devices, err := c.GetDevices(cmd.Context())
		if err != nil {
			return err
		}

		if len(devices) == 0 {
			fmt.Println("No devices registered")
			return nil
		}

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "DEVICE ID\tTYPE\tONLINE\tSELECTED\tAGENT VERSION\tREPORTED VERSION\tSTATE")
		for _, d := range devices {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%s\t%s\t%s\n",
				d.ID,
				d.Type,
				d.Online,
				d.Selected,
				valueOrDash(d.AgentVersion),
				valueOrDash(d.Version),
				valueOrDash(d.State),
			)
		}
		return tw.Flush()
	},
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.AddCommand(getDevicesCmd)
}
