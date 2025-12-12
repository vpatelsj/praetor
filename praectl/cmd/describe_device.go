package cmd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var describeCmd = &cobra.Command{
	Use:   "describe",
	Short: "Describe Praetor resources",
}

var describeDeviceCmd = &cobra.Command{
	Use:   "device <deviceType> <id>",
	Short: "Describe a single device",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceType := strings.ToLower(args[0])
		deviceID := args[1]
		c := newClient()

		device, err := c.GetDeviceByType(cmd.Context(), deviceType, deviceID)
		if err != nil {
			return err
		}

		fmt.Printf("ID:\t%s\n", device.ID)
		fmt.Printf("Type:\t%s\n", device.DeviceType)
		fmt.Printf("Online:\t%t\n", device.Online)
		fmt.Printf("Last Seen:\t%s\n", device.LastSeen.Format(time.RFC3339))

		if len(device.Labels) > 0 {
			fmt.Println("Labels:")
			for _, line := range renderSortedMap(device.Labels) {
				fmt.Printf("  %s\n", line)
			}
		} else {
			fmt.Println("Labels: <none>")
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(describeCmd)
	describeCmd.AddCommand(describeDeviceCmd)
}

func renderSortedMap(m map[string]string) []string {
	pairs := make([]string, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(pairs)
	return pairs
}
