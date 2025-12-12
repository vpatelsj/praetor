package cmd

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"praectl/pkg/client"
)

var (
	serverURL string
)

var rootCmd = &cobra.Command{
	Use:   "praectl",
	Short: "Command line interface for interacting with the Praetor manager",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if serverURL == "" {
			return fmt.Errorf("server address cannot be empty")
		}
		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&serverURL, "server", "http://localhost:8080", "Praetor manager server address")
}

func newClient() *client.PraetorClient {
	return client.NewPraetorClient(serverURL, &http.Client{Timeout: 15 * time.Second})
}
