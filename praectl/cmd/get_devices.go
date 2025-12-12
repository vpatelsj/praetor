package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Display Praetor resources",
}

var (
	getDevicesType string
)

var getDevicesCmd = &cobra.Command{
	Use:   "devices",
	Short: "List registered devices",
	RunE: func(cmd *cobra.Command, args []string) error {
		if getDevicesType == "" {
			return fmt.Errorf("--type is required (e.g. --type switch)")
		}
		c := newClient()
		devices, err := c.GetDevicesByType(cmd.Context(), strings.ToLower(getDevicesType))
		if err != nil {
			return err
		}

		if len(devices) == 0 {
			fmt.Println("No devices registered")
			return nil
		}

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "DEVICE ID\tTYPE\tONLINE\tLAST SEEN")
		for _, d := range devices {
			fmt.Fprintf(tw, "%s\t%s\t%t\t%s\n",
				d.ID,
				d.DeviceType,
				d.Online,
				d.LastSeen.Format(time.RFC3339),
			)
		}
		return tw.Flush()
	},
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.AddCommand(getDevicesCmd)
	getDevicesCmd.Flags().StringVar(&getDevicesType, "type", "", "Device type to query (switch, dpu, soc, bmc)")
}
