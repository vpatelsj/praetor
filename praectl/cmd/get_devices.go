package cmd

import (
	"fmt"
	"sort"
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
	getDevicesTypes []string
)

var getDevicesCmd = &cobra.Command{
	Use:   "devices",
	Short: "List registered devices",
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()

		types := getDevicesTypes
		if len(types) == 0 {
			types = []string{"switch", "bmc", "dpu", "soc"}
		}

		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
		printedHeader := false

		for _, t := range types {
			devices, err := c.GetDevicesByType(cmd.Context(), strings.ToLower(t))
			if err != nil {
				return err
			}
			if len(devices) == 0 {
				continue
			}
			if !printedHeader {
				fmt.Fprintln(tw, "DEVICE ID\tTYPE\tONLINE\tLAST SEEN\tLABELS")
				printedHeader = true
			}
			for _, d := range devices {
				fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\n",
					d.ID,
					d.DeviceType,
					d.Online,
					d.LastSeen.Format(time.RFC3339),
					renderLabels(d.Labels),
				)
			}
		}

		if !printedHeader {
			fmt.Println("No devices registered")
			return nil
		}

		return tw.Flush()
	},
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.AddCommand(getDevicesCmd)
	getDevicesCmd.Flags().StringSliceVar(&getDevicesTypes, "type", nil, "Device type(s) to query (switch, dpu, soc, bmc). If omitted, all are queried.")
}

func renderLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "<none>"
	}
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}
