package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

var describeCmd = &cobra.Command{
	Use:   "describe",
	Short: "Describe Praetor resources",
}

var describeDeviceCmd = &cobra.Command{
	Use:   "device <id>",
	Short: "Describe a single device",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deviceID := args[0]
		c := newClient()

		device, err := c.GetDevice(cmd.Context(), deviceID)
		if err != nil {
			return err
		}

		fmt.Printf("ID:\t%s\n", device.ID)
		fmt.Printf("Type:\t%s\n", device.Type)
		fmt.Printf("Online:\t%t\n", device.Online)
		fmt.Printf("Selected:\t%t\n", device.Selected)
		fmt.Printf("Agent Version:\t%s\n", valueOrDash(device.AgentVersion))
		fmt.Printf("Reported Version:\t%s\n", valueOrDash(device.Version))
		fmt.Printf("State:\t%s\n", valueOrDash(device.State))
		if device.Message != "" {
			fmt.Printf("Message:\t%s\n", device.Message)
		}
		fmt.Printf("Registered:\t%s\n", formatTime(device.RegisteredAt))
		fmt.Printf("Last Seen:\t%s\n", formatTime(device.LastSeen))

		if len(device.Labels) > 0 {
			fmt.Println("Labels:")
			for _, line := range renderSortedMap(device.Labels) {
				fmt.Printf("  %s\n", line)
			}
		} else {
			fmt.Println("Labels: <none>")
		}

		if len(device.Capabilities) > 0 {
			fmt.Println("Capabilities:")
			for _, cap := range device.Capabilities {
				fmt.Printf("  - %s\n", cap)
			}
		} else {
			fmt.Println("Capabilities: <none>")
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

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}
