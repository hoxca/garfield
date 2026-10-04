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
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "",
		"fichier de configuration YAML (défaut : "+configRelPath+" s'il existe)")

	// Loading here rather than in each command keeps the config file invisible
	// to them: by the time RunE runs, every flag already carries its final
	// value. It is the closest PersistentPreRunE to every subcommand, and none
	// of them defines its own.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		return applyConfig(cmd)
	}

	// cobra returns on the help flag before any PreRun hook, so the config
	// would never load and --help would advertise defaults the command is not
	// using. HelpFunc() yields cobra's own renderer, captured here before it is
	// replaced; subcommands inherit this through the parent chain.
	renderHelp := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if err := applyConfig(cmd); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		renderHelp(cmd, args)
	})

	rootCmd.AddCommand(analyzeCmd)
	rootCmd.AddCommand(prepareCmd)
	rootCmd.AddCommand(versionCmd)

	// thresholds and concurrency are read by both commands so they can never
	// disagree about what a usable frame is or how much work to do. The path
	// sections are per-command because --output means different things.
	setConfigSections(analyzeCmd, sectionThresholds, sectionConcurrency, sectionAnalyze)
	setConfigSections(prepareCmd, sectionThresholds, sectionConcurrency, sectionPrepare)
}
