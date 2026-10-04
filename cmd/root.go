package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags "-X garfield/cmd.Version=...".
// Defaults to dev for local builds.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "garfield",
	Short: "Analyse de qualité d'images astro (FITS)",
	Long: `Garfield analyse des images FITS pour évaluer leur qualité :
étoiles détectées, FWHM, excentricité, SNR et score global.

Exemples :
  garfield analyze images/
  garfield analyze --dir images/ --min-snr 11`,
	Version: Version,
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(analyzeCmd)
	rootCmd.AddCommand(prepareCmd)
	rootCmd.AddCommand(versionCmd)
}
