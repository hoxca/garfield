package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Affiche la version de garfield",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("garfield %s\n", Version)
	},
}
