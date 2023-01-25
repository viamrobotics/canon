package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show configuration",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Active Profile: %+v\n", activeProfile)
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}
