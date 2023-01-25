package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// configCmd represents the config command
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show configuration",
	Long:  ``,
	Run: func(cmd *cobra.Command, args []string) {
		ret, err := yaml.Marshal(activeProfile)
		fmt.Printf("Active Profile:\n%s", ret)
		cobra.CheckErr(err)
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
}
