package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config file layout.
//
// The keys are "<section>.<flag-name>", so no translation table is needed
// between the YAML and the flags. Sections exist because flag names collide
// across commands: --output is a CSV file for analyze but a destination
// directory for prepare, so those two keys must not share a namespace.
//
// Sections read by both commands are thresholds and concurrency; prepare and
// analyze hold the paths each one defaults to.
const (
	sectionThresholds  = "thresholds"
	sectionConcurrency = "concurrency"
	sectionPrepare     = "prepare"
	sectionAnalyze     = "analyze"
)

// configRelPath is the working-directory-relative location, the one a checkout
// of this repository ships. The remaining candidates let an installed binary
// pick up a per-user override from anywhere.
const configRelPath = "conf/garfield.yaml"

// configMarker tags a flag whose default came from the config file. Only the
// file name is shown: cobra appends "(default ...)" after the usage text, so a
// full path here pushes the real default off the edge of the line.
const configMarker = "[config: "

// configPath is bound to the root --config flag. Empty means "search".
var configPath string

// configExcluded lists flags a section must never drive. analyze's --output is
// a CSV file for a single run, not a standing default; leaving it reachable
// would let a value aimed at one command be written to a report path. prepare's
// --output is excluded from analyze for the same reason, though that is already
// prevented by the section split.
var configExcluded = map[string]map[string]bool{
	sectionAnalyze: {"output": true},
}

// excluded reports whether a section must leave a flag alone.
func excluded(section, flag string) bool {
	return configExcluded[section][flag]
}

// configSections maps a command to the config sections it reads. Populated in
// root.go's init because the commands live in their own files.
var configSections = map[*cobra.Command][]string{}

// setConfigSections records which sections a command reads. Tests use it to
// register throwaway commands through the same path production does.
func setConfigSections(cmd *cobra.Command, sections ...string) {
	configSections[cmd] = sections
}

// resolveConfigPath returns the config file to load, or "" when there is none.
// An explicit --config that does not exist is an error rather than a silent
// fallback: the user asked for that file specifically, and quietly using the
// built-in defaults instead would hide a typo in the path.
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("fichier de configuration %s : %w", explicit, err)
		}
		return explicit, nil
	}

	for _, candidate := range configCandidates() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	// No config file is a supported way to run: the compiled-in defaults in
	// thresholds.go and prepare.go apply.
	return "", nil
}

// configCandidates lists the paths searched, in order.
func configCandidates() []string {
	candidates := []string{configRelPath}

	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "garfield", "garfield.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "garfield", "garfield.yaml"))
	}
	return candidates
}

// readConfig loads the file. A malformed file is fatal rather than ignored: it
// holds the acceptance criteria deciding which frames are discarded, so
// quietly falling back to the built-in defaults is the dangerous outcome.
func readConfig(path string) (*viper.Viper, error) {
	v := viper.New()
	v.SetConfigFile(path)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("lecture de la configuration %s : %w", path, err)
	}
	return v, nil
}

// applyConfig loads the config file, if there is one, and writes every value it
// supplies into the corresponding flag. Called from the root command's
// PersistentPreRunE so each command's options are final by the time RunE sees
// them, without any command needing to know a config file exists.
func applyConfig(cmd *cobra.Command) error {
	path, err := resolveConfigPath(configPath)
	if err != nil || path == "" {
		return err
	}

	v, err := readConfig(path)
	if err != nil {
		return err
	}
	warnUnknownKeys(v, path)

	return applySections(cmd, v, path)
}

// applySections pushes each configured value into its flag. A flag the user
// set explicitly is left alone, so the precedence is:
//
//	explicit flag  >  config file  >  compiled-in default
func applySections(cmd *cobra.Command, v *viper.Viper, path string) error {
	var errs []error

	for _, section := range configSections[cmd] {
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Changed {
				return // the command line wins
			}
			if excluded(section, f.Name) {
				return
			}
			key := section + "." + f.Name
			if !v.IsSet(key) {
				return
			}
			value := v.GetString(key)
			if err := f.Value.Set(value); err != nil {
				errs = append(errs, fmt.Errorf("%s : %w", key, err))
				return
			}
			// Reflect the effective value so --help does not claim a default
			// the command is not using. The marker is only appended once, since
			// a command can be configured twice when help renders after the
			// PreRun hook has already run.
			f.DefValue = value
			if !strings.Contains(f.Usage, configMarker) {
				f.Usage += " " + configMarker + filepath.Base(path) + "]"
			}
		})
	}

	if len(errs) > 0 {
		// Joined rather than reporting the first alone: a config with several
		// bad values should be fixed in one pass, not one key per run.
		return fmt.Errorf("configuration %s : %w", path, errors.Join(errs...))
	}
	return nil
}

// warnUnknownKeys reports keys the tool does not consume. The failure this
// guards against is a typo such as max_ecc or max-Ecc: viper would ignore it,
// the built-in default would stay in force, and the config would read as if the
// threshold had been changed.
//
// Keys a section deliberately refuses are reported separately from typos, so a
// value written on purpose is never silently dropped.
func warnUnknownKeys(v *viper.Viper, path string) {
	known := knownConfigKeys()

	var unknown, refused []string
	for _, key := range v.AllKeys() {
		switch {
		case known[key]:
		case isExcludedKey(key):
			refused = append(refused, key)
		default:
			unknown = append(unknown, key)
		}
	}

	sort.Strings(unknown)
	sort.Strings(refused)

	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr,
			"avertissement : %s contient des clés inconnues ignorées : %s\n",
			path, strings.Join(unknown, ", "))
	}
	if len(refused) > 0 {
		fmt.Fprintf(os.Stderr,
			"avertissement : %s contient des clés volontairement non configurables, ignorées : %s\n",
			path, strings.Join(refused, ", "))
	}
}

// isExcludedKey reports whether key names a flag a section refuses to drive.
func isExcludedKey(key string) bool {
	section, flag, found := strings.Cut(key, ".")
	return found && excluded(section, flag)
}

// knownConfigKeys collects every "<section>.<flag>" key any command reads, so a
// key belonging to a different command is not reported as unknown.
func knownConfigKeys() map[string]bool {
	known := map[string]bool{}

	for cmd, sections := range configSections {
		for _, section := range sections {
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				if excluded(section, f.Name) {
					return
				}
				known[section+"."+f.Name] = true
			})
		}
	}
	return known
}
