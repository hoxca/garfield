package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// TestCompiledDefaultsMatchShippedConfig pins the two places acceptance
// criteria live. Without it the binary applies one set when run from this
// repository and another when run anywhere else, which changes which frames are
// destroyed depending on the working directory -- the exact ambiguity the
// shared threshold type exists to prevent.
func TestCompiledDefaultsMatchShippedConfig(t *testing.T) {
	v := viper.New()
	v.SetConfigFile(filepath.Join("..", "conf", "garfield.yaml"))
	if err := v.ReadInConfig(); err != nil {
		t.Fatalf("reading conf/garfield.yaml: %v", err)
	}

	for _, tc := range []struct {
		key  string
		want float64
	}{
		{"thresholds.min-snr", defaultThresholds.minSNR},
		{"thresholds.max-fwhm", defaultThresholds.maxFWHM},
		{"thresholds.max-ecc", defaultThresholds.maxEcc},
		{"thresholds.min-score", defaultThresholds.minScore},
		{"thresholds.min-stars", float64(defaultThresholds.minStars)},
		{"thresholds.max-fwhm-sigma", defaultThresholds.maxFWHMSigma},
		{"thresholds.max-halo-ratio", defaultThresholds.maxHaloRatio},
		{"thresholds.max-structure", defaultThresholds.maxStructure},
	} {
		if !v.IsSet(tc.key) {
			t.Errorf("conf/garfield.yaml does not set %s", tc.key)
			continue
		}
		if got := v.GetFloat64(tc.key); got != tc.want {
			t.Errorf("%s: config has %v, compiled default is %v", tc.key, got, tc.want)
		}
	}

	if got, want := v.GetBool("thresholds.moffat"), defaultThresholds.moffat; got != want {
		t.Errorf("thresholds.moffat: config has %v, compiled default is %v", got, want)
	}
}
