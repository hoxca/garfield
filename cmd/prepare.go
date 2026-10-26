package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"garfield/internal/mrs"

	"github.com/spf13/cobra"
)

// Default locations for the Kosmodrom acquisition tree. The input is the
// capture volume; the output is the scratch volume the prepared sessions are
// staged on before calibration is done with WBPP in Pixinsight.
const (
	defaultPrepareInput  = "/Volumes/Dyno/Kosmodrom"
	defaultPrepareOutput = "/Volumes/T7/Workspace"
)

// lightsDir, flatsDir and masterDir are the subdirectories of the input root holding
// lights, flat and calibration frames respectively.
const (
	lightsDir = "Lights"
	flatsDir  = "Flats"
	biasDir   = "Bias"
	darksDir  = "Darks"
)

// sessionLightsDir and sessionFlatsDir are the subdirectories of a prepared
// session.
const (
	sessionLightsDir = "lights"
	sessionFlatsDir  = "flats"
)

// biasOutDir holds the master biases for a target. It sits beside the session
// directories rather than inside one because a master bias is a property of the
// sensor setting, not of an observing night: one file serves every session
// taken at that gain. Nesting it per session would duplicate identical
// hundreds-megabyte files once per night.
const biasOutDir = "bias"

// darksOutDir holds the master darks, for the same reason and with the same
// shape as biasOutDir.
const darksOutDir = "darks"

// biasMastersDir and darksMastersDir are the acquisition-side directories
// holding the masters. The biases normally contain one file per gain; the darks are
// split into per-gain subdirectories, so indexing them recurses.
const (
	biasMastersDir  = "Masters"
	darksMastersDir = "Masters"
)

// rejectedDir sits beside the Sessions directory rather than inside a session,
// so a session tree holds only usable data and everything discarded is
// quarantined in one place.
const rejectedDir = "rejected"

// sessionsDir groups the prepared sessions, so it could be imported with one
// import directory <Sessions> in the WBPP Pixinsight process.
// the target root holds only Sessions/, rejected/ and metrics/.
const sessionsDir = "Sessions"

// metricsDir holds the CSV reports beside the session directories, keeping the
// target root to data directories only.
const metricsDir = "metrics"

// framesCSVName is the per-frame quality report, written under metricsDir.
const framesCSVName = "frames.csv"

// sessionsCSVName is the session index, written under metricsDir.
const sessionsCSVName = "sessions.csv"

// metricsPath returns the location of a report inside a prepared target.
func metricsPath(targetOut, name string) string {
	return filepath.Join(targetOut, metricsDir, name)
}

// sessionOutDir returns where a prepared session's data lives. The CSV reports
// record the bare session name; only the filesystem layout carries this
// wrapper, so consumers join on the session column rather than on a path.
func sessionOutDir(targetOut, sessionDir string) string {
	return filepath.Join(targetOut, sessionsDir, sessionDir)
}

// analyzeDefaultLimitComputedStars mirrors the analyze --limit-computed-stars
// default. prepare reuses processImage, which requires this to be set, but does
// not expose the flag itself.
const analyzeDefaultLimitComputedStars = 500

// sessionDateRe matches the YYYY-MM-DD directory names that identify an
// observing session.
var sessionDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// paRe extracts the position angle from a frame filename, e.g. "_PA310_E".
var paRe = regexp.MustCompile(`_PA(\d+)`)

// knownFilters are the colour filters the acquisition names, in a flat's
// filter token and in a light filter subdirectory. It is a membership set, not a
// display order, since filters are reported alphabetically. The token is a
// single character, so the set doubles as a character class for flatFilterRe
// below.
//
// The set is the filter wheel's inventory: seven positions, all occupied, in
// this order. Do not sort it. Nothing here depends on the sequence -- a
// character class is order-independent -- so the order is the only record of
// which physical position each letter names.
//
// Narrowband is therefore not a pending extension. FLAT_Ha_ and FLAT_OIII_ would
// need a different wheel to be acquired at all, not a letter added here, and
// the wheel is full at a remote site.
const knownFilters = "LRGBSHO"

// isKnownFilter reports whether f names one of the recognised colour filters.
// The length check is load-bearing: strings.Contains reports "BG" as a member
// of the set just as it does "B", and only single-character tokens are filters.
func isKnownFilter(f string) bool {
	return len(f) == 1 && strings.Contains(knownFilters, f)
}

// flatNamePrefix opens every flat filename. A name carrying it that
// flatFilterRe rejects is a flat garfield cannot use; a name without it is some
// other frame that merely shares the flats directory, so it is left alone.
const flatNamePrefix = "FLAT_"

// flatFilterRe extracts the filter from a flat frame filename, e.g.
// "FLAT_S_67.02s_...".
//
// knownFilters is spliced in as a character class, so the emitted pattern is
// ^FLAT_([LRGBSHO])_ : a single character drawn from the set, not the literal
// seven-letter string. The group stays capturing because indexFlats reads
// mf[1]; a non-capturing group, or dropping the brackets, would silently
// return nil for every flat.
var flatFilterRe = regexp.MustCompile(`^` + flatNamePrefix + `(` + `[` + knownFilters + `]` + `)_`)

// frameDateRe extracts the YYYYMMDD acquisition date embedded in a frame
// filename. It deliberately does not require the _PA token that follows on a C3
// frame: a G4 frame has no angle, and the date is the same field in both
// grammars. No profile branch is needed.
var frameDateRe = regexp.MustCompile(`_(\d{8})_\d{6}_\d+`)

// fitsExts are the filename extensions treated as FITS frames.
var fitsExts = map[string]bool{".FIT": true, ".FITS": true, ".FTS": true}

// masterExts are the extensions of the calibration masters. They are kept apart
// from fitsExts because the masters are in XISF pixinsight's format.
var masterExts = map[string]bool{".XISF": true}

// gainRe extracts the gain setting, "GA0" or "GA2750". Light frames and
// master calibrations use the same spelling, so the two match on the token
// directly: "..._-10C_GA0_20260914_..." against "masterBias_GA0_-10C_...".
var gainRe = regexp.MustCompile(`_(GA\d+)_`)

// lightComboRe extracts the exposure, sensor temperature setpoint and gain of a
// light frame, which together identify the master dark needed to calibrate it.
// The exposure is spelled the same way on both sides: "_600s_BIN1_..." against
// "masterDark_600s_...".
//
// The setpoint is matched, not assumed. It used to be hardcoded to "-10C", which
// meant a target shot at any other sensor temperature -- 0C is the obvious one --
// matched no dark at all, silently. A target may also mix setpoints across its
// sessions, so it is part of the key rather than an assumption about the
// instrument.
//
// Note the token order differs from the dark side: here it is
// exposure/setpoint/gain, there exposure/gain/setpoint.
var lightComboRe = regexp.MustCompile(`_(\d+)s_BIN\d_([-+]?\d+C)_(GA\d+)_`)

// darkComboRe extracts the exposure, gain and setpoint from a master dark
// filename. The group order is exposure, gain, setpoint -- the reverse of
// lightComboRe, because the acquisition names the two kinds of frame
// differently. Compare m[1] with care against the light side.
var darkComboRe = regexp.MustCompile(`masterDark_(\d+)s_(GA\d+)_([-+]?\d+C)_`)

// normaliseTemp canonicalises a sensor temperature setpoint token for use as a
// key component, so that "-0C" and "0C" -- the same temperature, signed two ways
// -- are one key rather than two that can never meet. Zero is the only value
// where the sign is ambiguous; elsewhere "-10C" and "10C" are genuinely different
// temperatures and must stay apart.
//
// Only zero is collapsed. Any other token is returned as written, so the key
// carries the acquisition's own spelling rather than a reconstruction of it.
func normaliseTemp(token string) string {
	digits := strings.TrimSuffix(strings.TrimLeft(token, "+-"), "C")
	n, err := strconv.Atoi(digits)
	if err != nil || n != 0 {
		return token
	}
	return "0C"
}

// masterDateRe extracts the trailing date suffix a master carries, so the most
// recently made master of a kind can be preferred when there are several.
var masterDateRe = regexp.MustCompile(`_(\d{8})\.xisf$`)

// g4FlatFilterRe extracts the filter from a Moravian G4 flat, whose name carries
// an unused prefix before _FLAT_. The prefix is required (".+"), which is what
// keeps it from matching a C3 flat: those begin with FLAT_, so they contain no
// "_FLAT_" substring at all.
var g4FlatFilterRe = regexp.MustCompile(`^.+_FLAT_([` + knownFilters + `])_`)

// g4LightComboRe extracts the exposure and setpoint of a G4 light. The third
// group is a frame index where a C3 frame carries its gain; it is captured only
// because it terminates the setpoint token, and is otherwise unread.
var g4LightComboRe = regexp.MustCompile(`_(\d+)s_BIN\d_([-+]?\d+C)_(\d+)_`)

// g4DarkComboRe extracts the exposure and setpoint of a G4 master dark. There is
// no gain token, and no per-gain subdirectory on disk either: a CCD has one
// gain setting, so it is not part of the name or the key.
var g4DarkComboRe = regexp.MustCompile(`masterDark_(\d+)s_([-+]?\d+C)_`)

// g4BiasKeyRe extracts the setpoint that keys a G4 master bias. A C3 bias is
// keyed by gain instead; the two patterns cannot match each other's names,
// because each requires its token immediately after "masterBias_".
var g4BiasKeyRe = regexp.MustCompile(`masterBias_([-+]?\d+C)_`)

// cameraProfile is the filename convention and matching policy of one
// acquisition camera. The two are genuinely different instruments -- a C3 has a
// rotator and a gain setting, the G4 that preceded it has neither -- so the
// differences are selected here once rather than branched on at every call site.
//
// The patterns are deliberately disjoint: a file belonging to the other camera
// does not parse under the active profile and is left alone, which is the right
// outcome when both cameras' data share one acquisition tree.
type cameraProfile struct {
	name    string
	hasPA   bool // position angle in filenames, and used to match flats
	hasGain bool // gain token in filenames, and used to key masters

	// flatFilterRe extracts a flat's filter token as sub-match 1.
	flatFilterRe *regexp.Regexp
	// isFlat reports whether a name carries this camera's flat prefix. Used
	// only to decide whether a rejected name is worth reporting: a name that is
	// some other kind of frame should be left alone silently.
	isFlat func(name string) bool

	// lightDark returns the master dark key a light frame needs.
	lightDark func(name string) (darkKey, bool)
	// darkMaster returns the key a master dark file is indexed under.
	darkMaster func(base string) (darkKey, bool)
	// biasKey returns the key a master bias file is indexed under.
	biasKey func(name string) (string, bool)
}

// c3Camera is the Moravian C3: a rotator and a gain setting, both named in the
// filename and both used for matching.
var c3Camera = cameraProfile{
	name:         "c3",
	hasPA:        true,
	hasGain:      true,
	flatFilterRe: flatFilterRe,
	isFlat:       func(name string) bool { return strings.HasPrefix(name, flatNamePrefix) },
	lightDark: func(name string) (darkKey, bool) {
		m := lightComboRe.FindStringSubmatch(name)
		if m == nil {
			return darkKey{}, false
		}
		exposure, err := strconv.Atoi(m[1])
		if err != nil {
			return darkKey{}, false
		}
		return darkKey{gain: m[3], exposure: exposure, temp: normaliseTemp(m[2])}, true
	},
	darkMaster: func(base string) (darkKey, bool) {
		m := darkComboRe.FindStringSubmatch(base)
		if m == nil {
			return darkKey{}, false
		}
		exposure, err := strconv.Atoi(m[1])
		if err != nil {
			return darkKey{}, false
		}
		return darkKey{gain: m[2], exposure: exposure, temp: normaliseTemp(m[3])}, true
	},
	biasKey: func(name string) (string, bool) {
		m := gainRe.FindStringSubmatch(name)
		if m == nil {
			return "", false
		}
		return m[1], true
	},
}

// g4Camera is the Moravian G4: a CCD with no rotator and no gain setting. Its
// position angle is fixed by the imaging train, so every light and flat matches
// regardless of angle and none is read; its master calibration is keyed on the
// sensor temperature setpoint alone (plus exposure, for darks).
var g4Camera = cameraProfile{
	name:         "g4",
	hasPA:        false,
	hasGain:      false,
	flatFilterRe: g4FlatFilterRe,
	isFlat:       func(name string) bool { return strings.Contains(name, "_FLAT_") },
	lightDark: func(name string) (darkKey, bool) {
		m := g4LightComboRe.FindStringSubmatch(name)
		if m == nil {
			return darkKey{}, false
		}
		exposure, err := strconv.Atoi(m[1])
		if err != nil {
			return darkKey{}, false
		}
		return darkKey{exposure: exposure, temp: normaliseTemp(m[2])}, true
	},
	darkMaster: func(base string) (darkKey, bool) {
		m := g4DarkComboRe.FindStringSubmatch(base)
		if m == nil {
			return darkKey{}, false
		}
		exposure, err := strconv.Atoi(m[1])
		if err != nil {
			return darkKey{}, false
		}
		return darkKey{exposure: exposure, temp: normaliseTemp(m[2])}, true
	},
	biasKey: func(name string) (string, bool) {
		m := g4BiasKeyRe.FindStringSubmatch(name)
		if m == nil {
			return "", false
		}
		return normaliseTemp(m[1]), true
	},
}

// cameraProfiles maps the --camera values to their profiles. Order is not
// meaningful; the map is only a lookup.
var cameraProfiles = map[string]cameraProfile{
	"c3": c3Camera,
	"g4": g4Camera,
}

// cameraProfileFor resolves a --camera value. An unknown value is rejected
// rather than falling back to a default: silently preparing a target with the
// wrong matching rules would match flats across angles that never shared them,
// or find no calibration at all.
func cameraProfileFor(name string) (cameraProfile, error) {
	p, ok := cameraProfiles[name]
	if !ok {
		return cameraProfile{}, fmt.Errorf("unknown --camera %q, want c3 or g4", name)
	}
	return p, nil
}

// biasKeyNoun names what this camera keys a master bias on, for messages. The
// distinction matters: telling a G4 operator that a gain is missing would name
// a setting the camera does not have.
// gainNoun returns " and gain" for a camera whose dark key includes one, so a
// message on a CCD does not ask for a setting it does not have.
func (p cameraProfile) gainNoun() string {
	if p.hasGain {
		return "/gain"
	}
	return ""
}

func (p cameraProfile) biasKeyNoun() string {
	if p.hasGain {
		return "gain setting"
	}
	return "sensor temperature setpoint"
}

type prepareOptions struct {
	// Quality thresholds are shared with analyze, so a light frame is approved
	// here on exactly the criteria analyze would apply.
	qualityThresholds

	target       string
	input        string
	output       string
	camera       string // "c3" or "g4"; see cameraProfile
	dryRun       bool
	skipExisting bool
	quiet        bool

	// targetFlats is how many flats a session wants per filter and angle. Zero
	// means no goal at all: the session keeps whatever its own date holds and
	// never reaches into a neighbour.
	targetFlats int

	// workers and convWorkers control the quality pass; they mirror analyze's
	// flags and the mrs.ConvWorkers global.
	workers     int
	convWorkers int
}

var prepareOpts = prepareOptions{}

var prepareCmd = &cobra.Command{
	Use:   "prepare",
	Short: "Prepare a target's sessions for processing",
	Long: `Copies and reorganises the images of an astronomical target
from the acquisition tree into a working tree:

  <out>/<target>/Sessions/Session_NN/lights/<filter>/*.FIT
  <out>/<target>/Sessions/Session_NN/flats/<filter>/*.FIT
  <out>/<target>/rejected/Session_NN/<filter>/*.FIT

A session is a date directory under Lights/<target>.
Session numbers follow the chronological order of the dates.
Flats are selected by filter and by position angle
(PA), on the session date.

Every image is analysed before copying: only APPROVED images
go into lights/, the rest into rejected/. The quality
thresholds are those of the analyze command.

Example:
  garfield prepare --target LBN527
  garfield prepare -t LBN527 -i /Volumes/Dyno/Kosmodrom -o /Volumes/T7/Workspace
  garfield prepare -t M51 --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPrepare(prepareOpts)
	},
}

func init() {
	prepareCmd.Flags().StringVarP(&prepareOpts.target, "target", "t", "", "astronomical target (directory under Lights/)")
	prepareCmd.Flags().StringVarP(&prepareOpts.input, "input", "i", defaultPrepareInput, "root of the acquisition data")
	prepareCmd.Flags().StringVarP(&prepareOpts.output, "output", "o", defaultPrepareOutput, "destination root")
	prepareCmd.Flags().BoolVar(&prepareOpts.dryRun, "dry-run", false, "show the plan without copying")
	prepareCmd.Flags().BoolVar(&prepareOpts.skipExisting, "skip-existing", false, "do not overwrite existing files")
	prepareCmd.Flags().StringVar(&prepareOpts.camera, "camera", "c3",
		"camera the filenames come from: c3 (rotator and gain) or g4 (fixed angle, no gain)")
	prepareCmd.Flags().IntVar(&prepareOpts.targetFlats, "flat-count", 0,
		"flats to collect per filter and angle; 0 = the session's own date only")
	prepareCmd.Flags().BoolVar(&prepareOpts.quiet, "quiet", false, "suppress console output")

	registerQualityFlags(prepareCmd.Flags(), &prepareOpts.qualityThresholds)

	registerWorkerFlags(prepareCmd.Flags(), &prepareOpts.workers, &prepareOpts.convWorkers)
}

// sessionPlan is one observing night to prepare.
type sessionPlan struct {
	number int    // 1-based, drives the Session_NN directory name
	date   string // YYYY-MM-DD, the Lights source directory name
	pa     string // position angle shared by the session, digits only
	// filters holds the light filenames per filter, keyed by the Lights
	// filter subdirectory name.
	filters map[string][]string
	// flats holds the flats matched per filter, each recording the date
	// directory it was found in, which is not always the session's own.
	flats map[string][]flatRef
	// verdicts holds the quality decision per light filename, filled in by the
	// assessment pass before any copying happens.
	verdicts map[string]ImageResult
	// frameDates records the acquisition dates found inside the light
	// filenames. Sessions run past midnight, so these frequently disagree
	// with date; kept so the discrepancy stays visible in the report.
	frameDateMin string
	frameDateMax string
	// missingFlats lists filters that have lights but no matching flats.
	missingFlats []string
	// gains lists the camera gain settings the session's light frames were
	// taken at, sorted. Filters pair to gains in the acquisition, so a night
	// covering both a B/G/L/R set and an O/S set records two. Empty for a
	// camera that has no gain setting.
	gains []string
	// biasKeys lists what the session needs a master bias for: a gain on the
	// C3, a sensor temperature setpoint on the G4. Kept apart from gains
	// because the two coincide only on the C3.
	biasKeys []string
	// darksNeeded lists the master darks the session's approved frames need,
	// sorted by gain then exposure. Filled in after the assessment, since it
	// depends on which frames were approved.
	darksNeeded []darkKey
	lightCount  int
	flatCount   int
	// approvedCount and rejectedCount are filled in by the assessment pass.
	approvedCount int
	rejectedCount int
	// rejectReasons counts frames per decision, for the console summary.
	rejectReasons map[string]int
}

// cameraMismatchWarning reports lights that contradict the chosen --camera.
//
// A wrong --camera already fails loudly in the sense that nothing matches, but
// the individual warnings name filters and masters rather than the cause. This
// says the cause once, and names the likely fix.
func cameraMismatchWarning(sessions []sessionPlan, profile cameraProfile) string {
	var withPA, without int
	for _, s := range sessions {
		for _, filter := range s.filterNames() {
			for _, name := range s.filters[filter] {
				if paRe.MatchString(name) {
					withPA++
				} else {
					without++
				}
			}
		}
	}
	if profile.hasPA {
		if without > 0 && withPA == 0 {
			return fmt.Sprintf("--camera %s expects a position angle but none of the %d light frame(s) carry one; is this G4 data?",
				profile.name, without)
		}
		return ""
	}
	if withPA > 0 && without == 0 {
		return fmt.Sprintf("--camera %s ignores position angles but all %d light frame(s) carry one; is this C3 data?",
			profile.name, withPA)
	}
	return ""
}

// approved reports whether a light frame passed the quality check.
func (s sessionPlan) approved(name string) bool {
	r, ok := s.verdicts[name]
	if !ok {
		// No verdict recorded: treat as unapproved rather than silently
		// promoting an unanalysed frame into lights/.
		return false
	}
	return r.Error == nil && r.Decision == decisionApproved
}

// sessionDir returns the output directory name for the session. The underscore
// separator keeps the name a single shell- and glob-friendly token.
func (s sessionPlan) sessionDir() string {
	return fmt.Sprintf("Session_%02d", s.number)
}

// filterNames returns the session's filters in sorted order.
func (s sessionPlan) filterNames() []string {
	names := make([]string, 0, len(s.filters))
	for name := range s.filters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runPrepare(opts prepareOptions) error {
	if opts.target == "" {
		return fmt.Errorf("--target is required")
	}
	if opts.input == "" {
		return fmt.Errorf("--input cannot be empty")
	}
	if opts.output == "" {
		return fmt.Errorf("--output cannot be empty")
	}
	// Rejected rather than clamped: a negative count is a typo, and treating it
	// as zero would quietly turn borrowing off instead of saying so.
	if opts.targetFlats < 0 {
		return fmt.Errorf("--flat-count cannot be negative, got %d", opts.targetFlats)
	}
	profile, err := cameraProfileFor(opts.camera)
	if err != nil {
		return err
	}

	lightsRoot := filepath.Join(opts.input, lightsDir, opts.target)
	info, err := os.Stat(lightsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("target %q not found in %q", opts.target, filepath.Join(opts.input, lightsDir))
		}
		return fmt.Errorf("unable to access %q: %w", lightsRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", lightsRoot)
	}

	targetOut := filepath.Join(opts.output, opts.target)

	sessions, warnings, err := planSessions(lightsRoot, filepath.Join(opts.input, flatsDir), opts.target, opts.targetFlats, profile)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return fmt.Errorf("no session (YYYY-MM-DD directory) found in %q", lightsRoot)
	}
	if w := cameraMismatchWarning(sessions, profile); w != "" {
		warnings = append(warnings, w)
	}

	// Indexed once for the whole target: the masters are shared by every
	// session, so this is not per-session work.
	// A missing directory is not an error
	var masters map[string]biasMaster
	biasRoot := filepath.Join(opts.input, biasDir, biasMastersDir)
	if _, statErr := os.Stat(biasRoot); statErr == nil {
		var biasWarnings []string
		masters, biasWarnings, err = indexBiasMasters(biasRoot, profile)
		if err != nil {
			return err
		}
		warnings = append(warnings, biasWarnings...)
	}

	var darkMasters map[darkKey]darkMaster
	darkRoot := filepath.Join(opts.input, darksDir, darksMastersDir)
	if _, statErr := os.Stat(darkRoot); statErr == nil {
		var darkWarnings []string
		darkMasters, darkWarnings, err = indexDarkMasters(darkRoot, profile)
		if err != nil {
			return err
		}
		warnings = append(warnings, darkWarnings...)
	}

	if !opts.quiet {
		fmt.Printf("\n%s of %q (%d session(s)) into %q%s\n\n",
			prepareVerb(opts.dryRun), opts.target, len(sessions), targetOut, dryRunSuffix(opts.dryRun))
		if !opts.dryRun {
			fmt.Printf("Analysing the quality of %d image(s), this can take several minutes...\n\n",
				totalLightCount(sessions))
		}
	}

	// Assess every light frame before copying anything, so the destination
	// tree reflects the final decision for each frame.
	if err := assessSessions(sessions, opts); err != nil {
		return err
	}

	// After the assessment, because a master dark is only wanted for frames
	// that actually reach lights/.
	collectDarkKeys(sessions, profile)

	if !opts.dryRun {
		if err := os.MkdirAll(targetOut, 0o755); err != nil {
			return fmt.Errorf("unable to create %q: %w", targetOut, err)
		}
		if err := os.MkdirAll(filepath.Join(targetOut, metricsDir), 0o755); err != nil {
			return fmt.Errorf("unable to create %q: %w", filepath.Join(targetOut, metricsDir), err)
		}
	}

	totalLights, totalFlats, totalBias, totalDarks := 0, 0, 0, 0
	for _, s := range sessions {
		copiedLights, copiedFlats, err := prepareSession(s, targetOut, opts)
		if err != nil {
			return err
		}
		totalLights += copiedLights
		totalFlats += copiedFlats

		if !opts.quiet {
			detail := fmt.Sprintf("%s (%s)", s.sessionDir(), s.date)
			if s.pa != "" {
				detail += fmt.Sprintf(" PA%s", s.pa)
			}
			detail += fmt.Sprintf(": %d approved, %d rejected [%s]",
				s.approvedCount, s.rejectedCount, strings.Join(s.filterNames(), " "))
			if copiedFlats > 0 {
				detail += fmt.Sprintf(", %d flat(s)", copiedFlats)
			} else if len(s.missingFlats) > 0 {
				detail += fmt.Sprintf(", no flat for [%s]", strings.Join(s.missingFlats, " "))
			}
			if opts.dryRun {
				detail = "(dry run) " + detail
			}
			fmt.Println("  " + detail)

			if reason := s.dominantRejectReason(); reason != "" {
				fmt.Printf("      reject reason: %s\n", reason)
			}
		}
	}

	// Copied once for the target, after the sessions, because the masters are
	// shared: one file per gain serves every night taken at that setting.
	biasCount, biasWarnings, err := prepareBias(targetOut, targetBiasKeys(sessions), masters, opts, profile)
	if err != nil {
		return err
	}
	totalBias = biasCount
	warnings = append(warnings, biasWarnings...)

	darkKeys, darkNeed := targetDarkKeys(sessions)
	darkCount, darkWarnings, err := prepareDarks(targetOut, darkKeys, darkNeed, darkMasters, opts)
	if err != nil {
		return err
	}
	totalDarks = darkCount
	warnings = append(warnings, darkWarnings...)

	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
	}

	approved, rejected := 0, 0
	for _, s := range sessions {
		approved += s.approvedCount
		rejected += s.rejectedCount
	}

	if !opts.quiet {
		fmt.Printf("\n%s: %d session(s), %d approved image(s), %d rejected image(s), %d flat(s), %d master bias, %d master dark.\n",
			finishVerb(opts.dryRun), len(sessions), approved, rejected, totalFlats, totalBias, totalDarks)
		if rejected > 0 && !opts.dryRun {
			fmt.Printf("Rejected images under %q.\n", filepath.Join(targetOut, rejectedDir))
		}
		if totalBias > 0 {
			fmt.Printf("Master bias under %q.\n",
				filepath.Join(sessionOutDir(targetOut, ""), biasOutDir))
		}
		if totalDarks > 0 {
			fmt.Printf("Master dark under %q.\n",
				filepath.Join(sessionOutDir(targetOut, ""), darksOutDir))
		}
		if !opts.dryRun {
			fmt.Printf("Index written to %q.\n", metricsPath(targetOut, sessionsCSVName))
			fmt.Printf("Per-image metrics written to %q.\n", metricsPath(targetOut, framesCSVName))
		}
	}

	if opts.dryRun {
		return nil
	}
	if err := writeSessionsCSV(targetOut, sessions); err != nil {
		return err
	}
	return writeFramesCSV(targetOut, sessions)
}

// flatRef is one flat frame together with the date directory it was found in,
// which is not necessarily the session's own: a session short of flats borrows
// from a neighbouring night. The copy resolves its source from the recorded
// date, since rebuilding it from the session date would look in the wrong
// directory and fail to open.
type flatRef struct {
	date string
	name string
}

func dryRunSuffix(dry bool) string {
	if dry {
		return " [dry run]"
	}
	return ""
}

func finishVerb(dry bool) string {
	if dry {
		return "Dry run finished"
	}
	return "Preparation finished"
}

func prepareVerb(dry bool) string {
	if dry {
		return "Dry run"
	}
	return "Preparation"
}

// totalLightCount sums the light frames across every session.
func totalLightCount(sessions []sessionPlan) int {
	n := 0
	for _, s := range sessions {
		n += s.lightCount
	}
	return n
}

// dominantRejectReason returns the most common rejection reason for the
// session, with its count, or an empty string when nothing was rejected.
func (s sessionPlan) dominantRejectReason() string {
	best, bestN := "", 0
	// Iterate the reasons in a deterministic order so equal counts do not
	// produce run-to-run variation in the console output.
	for _, reason := range sortedReasons(s.rejectReasons) {
		if s.rejectReasons[reason] > bestN {
			best, bestN = reason, s.rejectReasons[reason]
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("%s (%d)", best, bestN)
}

func sortedReasons(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assessSessions runs the quality pipeline over every planned light frame,
// recording the verdict on each session. Frames are assessed across all sessions
// in one pass so the worker pool stays busy.
func assessSessions(sessions []sessionPlan, opts prepareOptions) error {
	type job struct {
		session int
		filter  string
		name    string
		path    string
	}

	var jobs []job
	for si := range sessions {
		for _, filter := range sessions[si].filterNames() {
			srcDir := filepath.Join(opts.input, lightsDir, opts.target, sessions[si].date, filter)
			for _, name := range sessions[si].filters[filter] {
				jobs = append(jobs, job{
					session: si,
					filter:  filter,
					name:    name,
					path:    filepath.Join(srcDir, name),
				})
			}
		}
	}
	if len(jobs) == 0 {
		return nil
	}

	for i := range sessions {
		sessions[i].verdicts = make(map[string]ImageResult, sessions[i].lightCount)
		sessions[i].rejectReasons = map[string]int{}
	}

	results := make([]ImageResult, len(jobs))

	// Mirror analyze's worker split: outer workers over frames, inner workers
	// inside the convolution, sized so the total stays near NumCPU.
	numCPU := runtime.NumCPU()
	workers := opts.workers
	if workers <= 0 {
		workers = numCPU / 2
		if workers < 2 {
			workers = 2
		}
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	convWorkers := opts.convWorkers
	if convWorkers <= 0 {
		convWorkers = numCPU / workers
		if convWorkers < 1 {
			convWorkers = 1
		}
	}
	mrs.ConvWorkers = convWorkers

	aopts := opts.qualityThresholds.analyze()

	index := make(chan int)
	var wg sync.WaitGroup
	var progressMu sync.Mutex
	done := 0

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range index {
				results[i] = processImage(jobs[i].path, aopts)

				if !opts.quiet && !opts.dryRun {
					progressMu.Lock()
					done++
					if done%50 == 0 || done == len(jobs) {
						fmt.Printf("\r  analyse : %d/%d image(s)", done, len(jobs))
					}
					progressMu.Unlock()
				}
			}
		}()
	}
	for i := range jobs {
		index <- i
	}
	close(index)
	wg.Wait()

	if !opts.quiet && !opts.dryRun {
		fmt.Println()
	}

	for i, j := range jobs {
		s := &sessions[j.session]
		s.verdicts[j.name] = results[i]

		approved := results[i].Error == nil && results[i].Decision == decisionApproved
		if approved {
			s.approvedCount++
			continue
		}
		s.rejectedCount++

		reason := results[i].Decision
		if results[i].Error != nil {
			reason = "ERREUR"
		} else if reason == "" {
			reason = "INCONNU"
		}
		s.rejectReasons[reason]++
	}

	return nil
}

// errMixedAngles marks a session holding frames at more than one position
// angle. It is a data fault rather than a fault of the run, so planSessions
// collects every such session and reports them together instead of stopping at
// the first: one pass then names everything that needs fixing.
var errMixedAngles = errors.New("mixed angles")

// planSessions walks the Lights target directory and builds a plan per session,
// pairing each with the flats of the same date, filter and position angle.
//
// Two position-angle faults are fatal rather than warnings, because both
// corrupt the calibration silently if they pass:
//
//   - a session carrying more than one angle, meaning the instrument was
//     rotated mid-session;
//   - sessions of one target disagreeing, meaning it was rotated between
//     nights. A target is acquired at a single angle chosen before the run
//     starts.
//
// In the second case each session would still be matched to its own date's
// flats, so every session would look internally consistent and the report
// would carry several different angles with nothing explaining why.
func planSessions(lightsRoot, flatsRoot, target string, targetFlats int, profile cameraProfile) ([]sessionPlan, []string, error) {
	entries, err := os.ReadDir(lightsRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to read %q: %w", lightsRoot, err)
	}

	var dates []string
	for _, e := range entries {
		if !e.IsDir() || !sessionDateRe.MatchString(e.Name()) {
			continue
		}
		dates = append(dates, e.Name())
	}
	sort.Strings(dates)

	var (
		sessions []sessionPlan
		warnings []string
		fatal    []string
	)

	for i, date := range dates {
		s, warns, err := planSession(filepath.Join(lightsRoot, date), flatsRoot, i+1, date, dates, targetFlats, profile)
		if err != nil {
			// A fault in the data is collected and reported with the others; a
			// directory that could not be read is not, since planning on from
			// an incomplete view would be worse than stopping.
			if errors.Is(err, errMixedAngles) {
				// Unwrapped before collecting, so the batched message does not
				// repeat the sentinel's own text on every entry.
				fatal = append(fatal, strings.TrimPrefix(err.Error(), errMixedAngles.Error()+": "))
				continue
			}
			return nil, nil, err
		}
		sessions = append(sessions, s)
		warnings = append(warnings, warns...)
	}

	if msg := mixedAnglesAcrossSessions(sessions, target); msg != "" {
		fatal = append(fatal, msg)
	}

	if len(fatal) > 0 {
		return nil, nil, fmt.Errorf("%s: %s", errMixedAngles, strings.Join(fatal, "; "))
	}

	return sessions, warnings, nil
}

// mixedAnglesAcrossSessions reports a target whose sessions do not all share
// one position angle, or "" when they do. Returns a message rather than a bare
// bool because the set of angles is what the operator needs: there is no
// principled "correct" angle to name, and saying which one is expected would be
// arbitrary when the target has been rotated more than once.
//
// Sessions with no angle at all are left out: an empty pa matches no flat, so
// they already surface as a missing-flat warning, and treating that as fatal
// would be a separate change.
func mixedAnglesAcrossSessions(sessions []sessionPlan, target string) string {
	seen := map[string]bool{}
	for _, s := range sessions {
		if s.pa != "" {
			seen[s.pa] = true
		}
	}
	if len(seen) < 2 {
		return ""
	}

	angles := make([]string, 0, len(seen))
	for a := range seen {
		angles = append(angles, a)
	}
	sort.Strings(angles)

	return fmt.Sprintf("target %s is acquired at %d different angles (%s), want one",
		target, len(angles), strings.Join(angles, "/"))
}

// planSession builds the plan for a single session directory.
//
// flatsRoot is the Flats directory of the acquisition tree, not this session's
// flat directory: a session short of flats borrows from its neighbours, so the
// neighbouring dates have to be reachable. targetDates is every date directory
// this target acquired on, in order, and bounds where a borrowed flat may come
// from -- only dates the target itself observed.
//
// targetFlats is how many flats the session wants per filter and angle. At zero
// there is no goal: the session keeps its own date's frames, reaches into no
// neighbour, and is never reported short.
func planSession(sessionDir, flatsRoot string, number int, date string, targetDates []string, targetFlats int, profile cameraProfile) (sessionPlan, []string, error) {
	s := sessionPlan{
		number:  number,
		date:    date,
		filters: map[string][]string{},
		flats:   map[string][]flatRef{},
	}

	filterDirs, filterWarns, err := filterSubdirs(sessionDir)
	if err != nil {
		return s, nil, err
	}

	warnings := filterWarns
	paSeen := map[string]bool{}
	gainSeen := map[string]bool{}
	biasSeen := map[string]bool{}

	for _, filter := range filterDirs {
		files, err := listFITS(filepath.Join(sessionDir, filter))
		if err != nil {
			return s, nil, err
		}
		if len(files) == 0 {
			continue
		}
		s.filters[filter] = files
		s.lightCount += len(files)

		for _, f := range files {
			if m := frameDateRe.FindStringSubmatch(f); m != nil {
				s.trackFrameDate(formatFrameDate(m[1]))
			}
			// A camera without a rotator has no angle to record, and no gain
			// setting either. Reading them anyway would invent a session
			// property the instrument does not have.
			if profile.hasPA {
				if m := paRe.FindStringSubmatch(f); m != nil {
					paSeen[m[1]] = true
				}
			}
			if profile.hasGain {
				if m := gainRe.FindStringSubmatch(f); m != nil {
					gainSeen[m[1]] = true
					biasSeen[m[1]] = true
				}
			} else if key, ok := profile.lightDark(f); ok {
				// Without a gain token the bias is keyed on the setpoint, so
				// that is what this session needs a master for.
				biasSeen[key.temp] = true
			}
		}
	}

	// Recorded in sorted order so the report and the console are stable.
	s.gains = make([]string, 0, len(gainSeen))
	for g := range gainSeen {
		s.gains = append(s.gains, g)
	}
	sort.Strings(s.gains)

	s.biasKeys = make([]string, 0, len(biasSeen))
	for k := range biasSeen {
		s.biasKeys = append(s.biasKeys, k)
	}
	sort.Strings(s.biasKeys)

	if len(paSeen) > 0 {
		angles := make([]string, 0, len(paSeen))
		for a := range paSeen {
			angles = append(angles, a)
		}
		sort.Strings(angles)
		if len(angles) > 1 {
			// A target is acquired at one angle chosen before the run starts,
			// so frames at a second angle mean the instrument was rotated
			// mid-session. Picking the lowest and carrying on would calibrate
			// part of the night against flats shot for a different angle, and
			// the report would name only the angle that happened to sort
			// first. Refuse instead, and leave s.pa unset so no half-built
			// session escapes with an arbitrary angle recorded.
			return s, nil, fmt.Errorf("%w: session %s (%s): multiple angles %s",
				errMixedAngles, s.sessionDir(), date, strings.Join(angles, "/"))
		}
		s.pa = angles[0]
	}

	// Flats are matched on the session date, not the date embedded in the
	// light filenames: sessions run past midnight, so the two disagree for
	// most nights and only the directory name lines up with Flats/.
	paLabel := ""
	if profile.hasPA {
		paLabel = fmt.Sprintf(" (PA%s)", s.pa)
	}
	for _, filter := range s.filterNames() {
		matched, searched := collectFlats(flatsRoot, date, s.pa, filter, targetDates, targetFlats, profile)
		switch {
		case len(matched) == 0:
			s.missingFlats = append(s.missingFlats, filter)
			warnings = append(warnings, fmt.Sprintf(
				"session %s (%s): no flat for filter %s%s, searched %s",
				s.sessionDir(), date, filter, paLabel, strings.Join(searched, ", ")))
		case targetFlats > 0 && len(matched) < targetFlats:
			// Short rather than absent: the frames are usable, so this warns
			// and continues. Naming the dates searched matters, because a
			// shortfall is otherwise indistinguishable from a night that
			// simply held fewer flats. Only reachable with a goal set -- at zero
			// there is no target to fall short of.
			warnings = append(warnings, fmt.Sprintf(
				"session %s (%s): %d flat(s) for filter %s%s, want %d, searched %s",
				s.sessionDir(), date, len(matched), filter, paLabel, targetFlats,
				strings.Join(searched, ", ")))
		}
		s.flats[filter] = matched
		s.flatCount += len(matched)
	}

	return s, warnings, nil
}

// collectFlats gathers up to target flats for one filter and angle,
// starting on the session's own date and then walking outward through the
// target's dates -- previous first, then next -- until the count is reached or
// the list runs out. The first session has no previous date, so the walk starts
// at the following one.
//
// Every candidate is keyed on filter and angle, so flats shot at another angle
// or for another filter are never borrowed. The returned dates are the ones
// actually consulted, for the shortfall warning to name.
//
// A target of zero means no goal at all, not a goal of zero: the session keeps
// its own date's frames and no neighbour is consulted. That distinction is
// carried explicitly in the two guards below. Testing len(matched) >= target
// unguarded would be true immediately at zero and hand back no flats at all,
// which would look like a night that shot none rather than a run that declined
// to borrow.
func collectFlats(flatRoot, date, pa, filter string, targetDates []string, target int, profile cameraProfile) ([]flatRef, []string) {
	var (
		matched  []flatRef
		searched []string
	)

	pos := -1
	for i, d := range targetDates {
		if d == date {
			pos = i
			break
		}
	}
	if pos < 0 {
		// The session's own date is not in the target's list, so there is
		// nothing to walk outward from. Reported as searched-but-empty.
		return nil, nil
	}

	// The session's own date first, then each neighbour in turn. Distance
	// grows while direction alternates back, next, back, next, so a night
	// missing flats does not stop the search at the first empty neighbour.
	for _, i := range flatSearchOrder(len(targetDates), pos) {
		d := targetDates[i]
		if target > 0 {
			searched = append(searched, d)
		}
		index, _ := indexFlats(filepath.Join(flatRoot, d), profile)
		for _, name := range index[flatKey(filter, pa)] {
			if target > 0 && len(matched) >= target {
				break
			}
			matched = append(matched, flatRef{date: d, name: name})
		}
		// Stop once the goal is met, or immediately after the session's own
		// date when there is no goal at all. Without the second clause a zero
		// target would keep walking the neighbours and return every flat they
		// hold, which is borrowing by accident.
		if target <= 0 || len(matched) >= target {
			break
		}
	}

	return matched, searched
}

// flatSearchOrder lists the positions of the session's own date followed by its
// neighbours, in the order they should be consulted: previous first, then next,
// stepping outward. For the first session there is no previous, so the walk
// begins at the next date.
func flatSearchOrder(n, pos int) []int {
	order := make([]int, 0, n)
	order = append(order, pos)
	for dist := 1; dist < n; dist++ {
		// Back before forward, so a session borrows from the night it followed
		// in preference to the one it precedes.
		if back := pos - dist; back >= 0 {
			order = append(order, back)
		}
		if fwd := pos + dist; fwd < n {
			order = append(order, fwd)
		}
	}
	return order
}

// trackFrameDate widens the recorded range of acquisition dates found in the
// light filenames.
func (s *sessionPlan) trackFrameDate(d string) {
	if s.frameDateMin == "" || d < s.frameDateMin {
		s.frameDateMin = d
	}
	if s.frameDateMax == "" || d > s.frameDateMax {
		s.frameDateMax = d
	}
}

// formatFrameDate converts a YYYYMMDD string to YYYY-MM-DD.
func formatFrameDate(v string) string {
	if len(v) != 8 {
		return v
	}
	return v[:4] + "-" + v[4:6] + "-" + v[6:8]
}

// filterSubdirs returns the light filter subdirectory names of a session,
// together with a warning for each directory that is not a known filter.
//
// An unrecognised directory is skipped rather than becoming a filter: left
// alone it would be carried into the plan as a filter with no flats, and the
// session would then report a missing flat for it, naming a filter the
// acquisition does not use. Reporting it as what it is -- a directory that is
// not a filter -- says more and keeps the report free of phantom filters.
func filterSubdirs(sessionDir string) ([]string, []string, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to read %q: %w", sessionDir, err)
	}
	var names []string
	var warnings []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !isKnownFilter(e.Name()) {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %q is not a known filter (%s), ignored",
				sessionDir, e.Name(), knownFilters))
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, warnings, nil
}

// flatKey identifies a flat set by filter and position angle.
func flatKey(filter, pa string) string {
	return filter + "@" + pa
}

// indexFlats groups the flat frames of one date by filter and angle, and
// returns a warning for each flat it had to drop.
//
// Only files that are actually flats are reported. A name that is not anchored
// on FLAT_ at all -- FLATL_H_..., a light frame, a master -- is a different
// kind of frame that happens to share the directory, and naming it as a broken
// flat would be misleading. A name that is anchored on FLAT_ but whose filter
// is outside the known set is a flat garfield cannot use, so it is reported.
func indexFlats(flatDir string, profile cameraProfile) (map[string][]string, []string) {
	index := map[string][]string{}
	var warnings []string

	entries, err := os.ReadDir(flatDir)
	if err != nil {
		// A missing or unreadable flats directory simply yields no flats; the
		// caller reports the gap per session.
		return index, warnings
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isFITSName(name) {
			continue
		}
		mf := profile.flatFilterRe.FindStringSubmatch(name)
		if mf == nil {
			// Recognised by its prefix rather than a second, looser regex: a
			// name carrying the prefix that the filter rejects is a flat
			// garfield cannot use, and naming it is how a newly added filter
			// announces itself. A name without the prefix -- FLATL_, a light
			// frame, a master, or the other camera's flat -- is a different
			// kind of frame sharing the directory, so reporting it as a broken
			// flat would be misleading.
			if profile.isFlat(name) {
				warnings = append(warnings, fmt.Sprintf(
					"%s: %q has no known filter (%s), ignored",
					flatDir, name, knownFilters))
			}
			continue
		}

		// A camera without a rotator writes no angle, and flats are matched on
		// filter alone. Keying on an empty angle is deliberate: it is what lets
		// every flat of that filter match, which is correct when the angle
		// never changes.
		pa := ""
		if profile.hasPA {
			mp := paRe.FindStringSubmatch(name)
			if mp == nil {
				warnings = append(warnings, fmt.Sprintf(
					"%s: %q has no position angle, ignored", flatDir, name))
				continue
			}
			pa = mp[1]
		}
		key := flatKey(mf[1], pa)
		index[key] = append(index[key], name)
	}

	for k := range index {
		sort.Strings(index[k])
	}
	sort.Strings(warnings)
	return index, warnings
}

func isFITSName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	return fitsExts[strings.ToUpper(filepath.Ext(name))]
}

// listFITS returns the sorted FITS filenames directly inside dir.
func listFITS(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("unable to read %q: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !isFITSName(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// prepareSession copies one session's lights and flats into the output tree,
// returning the number of light and flat files copied.
func prepareSession(s sessionPlan, targetOut string, opts prepareOptions) (int, int, error) {
	lights, flats := 0, 0

	sessionOut := sessionOutDir(targetOut, s.sessionDir())

	// Approved frames land inside the session under Sessions/; rejected ones are
	// hoisted to a rejected/ tree at the target root so a session directory
	// holds only usable data.
	for _, filter := range s.filterNames() {
		srcDir := filepath.Join(opts.input, lightsDir, opts.target, s.date, filter)

		for _, name := range s.filters[filter] {
			dst := filepath.Join(sessionOut, sessionLightsDir, filter, name)
			if !s.approved(name) {
				dst = filepath.Join(targetOut, rejectedDir, s.sessionDir(), filter, name)
			}
			if !opts.dryRun {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return lights, flats, fmt.Errorf("unable to create %q: %w", filepath.Dir(dst), err)
				}
			}
			n, err := copyFITS(filepath.Join(srcDir, name), dst, opts)
			if err != nil {
				return lights, flats, err
			}
			if n {
				lights++
			}
		}
	}

	flatRoot := filepath.Join(sessionOut, sessionFlatsDir)
	for _, filter := range s.filterNames() {
		matched := s.flats[filter]
		if len(matched) == 0 {
			continue
		}
		for _, ref := range matched {
			dst := filepath.Join(flatRoot, filter, ref.name)
			if !opts.dryRun {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return lights, flats, fmt.Errorf("unable to create %q: %w", filepath.Dir(dst), err)
				}
			}
			// From the date the flat was collected, which is not necessarily
			// the session's own: a session short of flats borrows from a
			// neighbour, and rebuilding this from s.date would look there and
			// fail to open.
			src := filepath.Join(opts.input, flatsDir, ref.date, ref.name)
			n, err := copyFITS(src, dst, opts)
			if err != nil {
				return lights, flats, err
			}
			if n {
				flats++
			}
		}
	}

	return lights, flats, nil
}

// copyBufferSize is the chunk size used when streaming a frame. Frames are
// around 120 MB, so copying them whole would spike memory across sessions.
const copyBufferSize = 1 << 20

// biasMaster is one master bias file and the key it is held under: the gain on a
// C3, the sensor temperature setpoint on the G4.
type biasMaster struct {
	key  string // "GA0"/"GA2750" on the C3, "-20C" on the G4
	date string // YYYYMMDD suffix, used to prefer the most recent
	name string // masterBias_GA0_-10C_257f_20251117.xisf
	src  string
}

// indexBiasMasters reads the master biases and keys them by gain. The
// acquisition names each file after the gain it was taken at, which is the same
// token the light frames carry, so a light frame's master is a lookup rather
// than a pattern rewrite.
//
// Two files for one gain would leave the choice arbitrary -- the names carry a
// date suffix, so a newer master can be added later -- so that is reported and
// the first in sorted order wins, which keeps the outcome reproducible.
func indexBiasMasters(dir string, profile cameraProfile) (map[string]biasMaster, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to read %q: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if keep, n := isMasterFile(e.Name()); keep {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	masters := map[string]biasMaster{}
	var warnings []string

	for _, name := range names {
		key, ok := profile.biasKey(name)
		if !ok {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %q ignored, no %s in the name", dir, name, profile.biasKeyNoun()))
			continue
		}
		date := masterDate(name)

		if prev, dup := masters[key]; dup {
			if !preferLater(prev.date, date) {
				if date == prev.date {
					warnings = append(warnings, tieWarning(dir, key, prev.name, name))
				} else {
					warnings = append(warnings,
						collisionWarning(dir, key, prev.name, name, prev.name))
				}
				continue
			}
			warnings = append(warnings,
				collisionWarning(dir, key, prev.name, name, name))
			masters[key] = biasMaster{key: key, date: date, name: name,
				src: filepath.Join(dir, name)}
			continue
		}

		masters[key] = biasMaster{key: key, date: date, name: name,
			src: filepath.Join(dir, name)}
	}

	return masters, warnings, nil
}

// targetGains returns the sorted union of the gains every session needs. A
// session mixing filters at two settings needs both masters: the acquisition
// pairs filters to gains, so a B/G/L/R night alongside an O/S night spans the
// two.
func targetBiasKeys(sessions []sessionPlan) []string {
	seen := map[string]bool{}
	for _, s := range sessions {
		for _, k := range s.biasKeys {
			seen[k] = true
		}
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// gainList renders a gain set for a message, or a placeholder when empty.
func keyList(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, " ")
}

// prepareBias copies the master biases the target's sessions need into a single
// shared directory under Sessions/. It runs once per target rather than once
// per session, so a target spanning two gains copies two files no matter how
// many nights it holds.
func prepareBias(targetOut string, keys []string, masters map[string]biasMaster, opts prepareOptions, profile cameraProfile) (int, []string, error) {
	if len(keys) == 0 {
		return 0, nil, nil
	}

	var (
		copied   int
		warnings []string
		dstRoot  = filepath.Join(sessionOutDir(targetOut, ""), biasOutDir)
		present  = make([]string, 0, len(masters))
	)
	for g := range masters {
		present = append(present, g)
	}
	sort.Strings(present)

	noun := profile.biasKeyNoun()
	var done []string
	for _, key := range keys {
		master, ok := masters[key]
		if !ok {
			// Same policy as a missing flat: reported, but the run continues,
			// because a session without its master is still usable data and the
			// reduction can supply the bias later.
			warnings = append(warnings, fmt.Sprintf(
				"no master bias for %s %s in %q (available: %s)",
				noun, key, filepath.Join(opts.input, biasDir, biasMastersDir), keyList(present)))
			continue
		}

		dst := filepath.Join(dstRoot, master.name)
		if !opts.dryRun {
			if err := os.MkdirAll(dstRoot, 0o755); err != nil {
				return copied, warnings, fmt.Errorf("unable to create %q: %w", dstRoot, err)
			}
		}
		n, err := copyFITS(master.src, dst, opts)
		if err != nil {
			return copied, warnings, err
		}
		if n {
			copied++
		}
		done = append(done, key)
	}

	if !opts.quiet && len(keys) > 0 {
		verb := prepareVerb(opts.dryRun)
		if copied == 0 {
			fmt.Printf("  %s: no master bias (%s)\n", verb, keyList(keys))
		} else {
			fmt.Printf("  %s: %d master bias for %s %s\n", verb, copied, noun, keyList(done))
		}
	}

	return copied, warnings, nil
}

// darkKey identifies a master dark by the three properties that make one
// necessary: the gain setting, the exposure time, and the sensor temperature
// setpoint. A target can mix all three freely -- SH2-54 takes 30s and 60s frames
// at GA0 and 600s frames at GA2750, the night of 2026-07-16 holds two of those
// at once, and a season can span two setpoints.
//
// The setpoint is part of the key because a dark is thermal: a -10C dark does
// not calibrate a 0C light. Leaving it out silently substitutes a dark shot at
// the wrong temperature, which is worse than supplying none.
//
// temp holds the setpoint token as the acquisition spelled it, after
// normaliseTemp so that "-0C" and "0C" collide on purpose rather than by
// accident.
type darkKey struct {
	gain     string
	exposure int
	temp     string
}

// String renders the triple the way the acquisition names it, "GA2750/600s/-10C".
// This is what the sessions.csv darks column and the missing-dark warning both
// show, so the third field is what tells two otherwise identical darks apart.
func (k darkKey) String() string {
	// A camera with no gain setting leaves the first field empty; rendering it
	// as "/900s/-20C" would look like a missing value rather than an absent
	// property.
	if k.gain == "" {
		return fmt.Sprintf("%ds/%s", k.exposure, k.temp)
	}
	return fmt.Sprintf("%s/%ds/%s", k.gain, k.exposure, k.temp)
}

// darkMaster is one master dark file and the combination it serves.
type darkMaster struct {
	key  darkKey
	date string // YYYYMMDD suffix, used to prefer the most recent
	name string
	src  string
}

// indexDarkMasters reads the master darks and keys them by gain and exposure.
//
// The acquisition splits these across per-gain subdirectories whose names
// disagree with the filenames inside them -- "G2750/" holding "GA2750" files --
// so the key is taken from the filename alone. Keying on the directory would
// produce "G2750", match nothing, and copy nothing at all.
func indexDarkMasters(root string, profile cameraProfile) (map[darkKey]darkMaster, []string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to read %q: %w", root, err)
	}

	var names []string
	for _, e := range entries {
		path := filepath.Join(root, e.Name())

		if e.IsDir() {
			// One level of per-gain subdirectories, then the files.
			sub, err := os.ReadDir(path)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to read %q: %w", path, err)
			}
			for _, se := range sub {
				if se.IsDir() {
					continue
				}
				if keep, n := isMasterFile(se.Name()); keep {
					names = append(names, filepath.Join(e.Name(), n))
				}
			}
			continue
		}
		if keep, n := isMasterFile(e.Name()); keep {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	masters := map[darkKey]darkMaster{}
	var warnings []string

	for _, rel := range names {
		base := filepath.Base(rel)
		key, ok := profile.darkMaster(base)
		if !ok {
			warnings = append(warnings, fmt.Sprintf(
				"%s: %q ignored, no exposure/setpoint%s in the name", root, base, profile.gainNoun()))
			continue
		}

		date := masterDate(base)

		if prev, dup := masters[key]; dup {
			if !preferLater(prev.date, date) {
				if date == prev.date {
					warnings = append(warnings, tieWarning(root, key.String(), prev.name, base))
				} else {
					warnings = append(warnings,
						collisionWarning(root, key.String(), prev.name, base, prev.name))
				}
				continue
			}
			warnings = append(warnings,
				collisionWarning(root, key.String(), prev.name, base, base))
			masters[key] = darkMaster{key: key, date: date, name: base,
				src: filepath.Join(root, rel)}
			continue
		}

		masters[key] = darkMaster{key: key, date: date, name: base,
			src: filepath.Join(root, rel)}
	}

	return masters, warnings, nil
}

// isMasterFile reports whether a directory entry is a master to consider,
// returning its name. Dotfiles are excluded: the syncthing client leaves
// ".syncthing.masterDark_....tmp" partials here, and copying a half-transferred
// master would be worse than having none. Only XISF counts, which also keeps
// the raw calibration frames under Darks/-10C/ out (G4 camera calibration files).
func isMasterFile(name string) (bool, string) {
	if strings.HasPrefix(name, ".") {
		return false, ""
	}
	if !masterExts[strings.ToUpper(filepath.Ext(name))] {
		return false, ""
	}
	return true, name
}

// masterDate reads the date suffix a master carries, empty when it has none.
// Both biases and darks are named "<...>_<YYYYMMDD>.xisf".
func masterDate(name string) string {
	if m := masterDateRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// preferLater reports whether a candidate master should replace the one already
// held. The most recently built master supersedes rather than competes, so a
// newer calibration replaces an older one of the same kind.
//
// Nothing else weighs in. A bias name carries a frame count ("257f") ahead of
// the date, which used to decide collisions purely by where it sorted; more
// frames is not better evidence of a current calibration, so it is not read at
// all. A tie therefore resolves to neither candidate on merit, and the caller
// keeps the one it already holds -- deterministic only because candidates are
// visited in sorted order, which is why an undecided tie is reported as such.
func preferLater(heldDate, candidateDate string) bool {
	return candidateDate > heldDate
}

// collisionWarning reports two masters for one key. Reported whichever way it
// resolves, because a duplicate master is an anomaly worth surfacing even when
// the choice between them is unambiguous.
func collisionWarning(dir, key, held, candidate, kept string) string {
	return fmt.Sprintf("%s: two masters for %s (%s and %s), retaining %s",
		dir, key, held, candidate, kept)
}

// tieWarning reports two masters for one key whose dates are equal. The date is
// the only quality signal, so it does not choose between them, and saying
// "retained" would imply it had. What is kept is only the first in sorted order.
func tieWarning(dir, key, held, candidate string) string {
	return fmt.Sprintf(
		"%s: two masters for %s at the same date (%s and %s), date insufficient to decide, kept %s by name order",
		dir, key, held, candidate, held)
}

// lightDarkKey returns the master dark combination a light frame needs, and
// whether the filename carried one at all. The three tokens come out in a
// different order from darkComboRe's, so the groups are read here rather than
// shared: m[1] exposure, m[2] sensor temperature setpoint, m[3] gain.
func lightDarkKey(name string) (darkKey, bool) {
	return c3Camera.lightDark(name)
}

// collectDarkKeys records, per session, the master darks for its approved frames.
// Rejected frames are excluded on purpose: a dark for frames that never
// reach lights/ is 233 MB of nothing.
//
// This runs after the assessment, unlike the gains, which are read from the
// plan before any verdict is known.
func collectDarkKeys(sessions []sessionPlan, profile cameraProfile) {
	for i := range sessions {
		s := &sessions[i]
		seen := map[darkKey]bool{}

		for _, filter := range s.filterNames() {
			for _, name := range s.filters[filter] {
				if !s.approved(name) {
					continue
				}
				if key, ok := profile.lightDark(name); ok {
					seen[key] = true
				}
			}
		}

		s.darksNeeded = make([]darkKey, 0, len(seen))
		for k := range seen {
			s.darksNeeded = append(s.darksNeeded, k)
		}
		sort.Slice(s.darksNeeded, func(a, b int) bool {
			if s.darksNeeded[a].gain != s.darksNeeded[b].gain {
				return s.darksNeeded[a].gain < s.darksNeeded[b].gain
			}
			return s.darksNeeded[a].exposure < s.darksNeeded[b].exposure
		})
	}
}

// targetDarkKeys returns the sorted union of the master darks every session
// needs, and for each one the sessions that need it, so a missing master can be
// reported against the nights it affects.
func targetDarkKeys(sessions []sessionPlan) ([]darkKey, map[darkKey][]string) {
	need := map[darkKey][]string{}

	for _, s := range sessions {
		for _, k := range s.darksNeeded {
			need[k] = append(need[k], s.sessionDir())
		}
	}

	keys := make([]darkKey, 0, len(need))
	for k := range need {
		keys = append(keys, k)
	}
	// The setpoint is the last comparison, and it has to be here at all: without
	// it two keys differing only in sensor temperature tie, and sort.Slice is not
	// stable, so the darks column of the report and the available list of a
	// missing-dark warning would come out in a different order from one run to
	// the next.
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].gain != keys[b].gain {
			return keys[a].gain < keys[b].gain
		}
		if keys[a].exposure != keys[b].exposure {
			return keys[a].exposure < keys[b].exposure
		}
		return keys[a].temp < keys[b].temp
	})
	return keys, need
}

// prepareDarks copies the master darks the target's approved frames need into a
// shared directory under Sessions/, once for the whole target.
func prepareDarks(targetOut string, keys []darkKey, need map[darkKey][]string,
	masters map[darkKey]darkMaster, opts prepareOptions) (int, []string, error) {

	var (
		copied   int
		warnings []string
		dstRoot  = filepath.Join(sessionOutDir(targetOut, ""), darksOutDir)
		present  = make([]string, 0, len(masters))
		done     []string
	)
	for k := range masters {
		present = append(present, k.String())
	}
	sort.Strings(present)

	for _, key := range keys {
		master, ok := masters[key]
		if !ok {
			// Same policy as a missing flat or bias: reported, but the run
			// continues, because the frames are still usable data and the dark
			// can be shot or supplied later.
			warnings = append(warnings, fmt.Sprintf(
				"no master dark for %s in %q (available: %s), affected sessions: %s",
				key, filepath.Join(opts.input, darksDir, darksMastersDir),
				strings.Join(present, " "), strings.Join(need[key], " ")))
			continue
		}

		dst := filepath.Join(dstRoot, master.name)
		if !opts.dryRun {
			if err := os.MkdirAll(dstRoot, 0o755); err != nil {
				return copied, warnings, fmt.Errorf("unable to create %q: %w", dstRoot, err)
			}
		}
		n, err := copyFITS(master.src, dst, opts)
		if err != nil {
			return copied, warnings, err
		}
		if n {
			copied++
		}
		done = append(done, key.String())
	}

	if !opts.quiet && len(keys) > 0 {
		verb := prepareVerb(opts.dryRun)
		if copied == 0 {
			fmt.Printf("  %s: no master dark (%s)\n", verb, strings.Join(done, " "))
		} else {
			fmt.Printf("  %s: %d master dark for %s\n", verb, copied, strings.Join(done, " "))
		}
	}

	return copied, warnings, nil
}

// copyFITS copies one file, reporting whether it actually wrote anything. In
// dry-run mode nothing is touched, and with skip-existing an already-present
// destination is left alone.
func copyFITS(src, dst string, opts prepareOptions) (bool, error) {
	if opts.dryRun {
		return true, nil
	}
	if opts.skipExisting {
		if _, err := os.Stat(dst); err == nil {
			return false, nil
		}
	}

	in, err := os.Open(src)
	if err != nil {
		return false, fmt.Errorf("unable to open %q: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return false, fmt.Errorf("unable to create %q: %w", dst, err)
	}

	buf := make([]byte, copyBufferSize)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		out.Close()
		return false, fmt.Errorf("copy failed %q -> %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return false, fmt.Errorf("closing %q: %w", dst, err)
	}
	return true, nil
}

// framesCSVHeader describes one analysed light frame. It carries the session it
// belongs to rather than only the date, because a session directory is named
// for the evening it started while the frames themselves are usually stamped the
// following day.
var framesCSVHeader = []string{
	"session", "sessionDate", "pa", "filter",
	"filename", "detectedStars", "starCount",
	"avgFWHM", "avgSignal", "avgEccentricity", "snr", "score",
	"decision", "error",
}

// writeFramesCSV records the per-frame quality metrics that decided the split
// between lights/ and rejected/. Every analysed frame gets a row, approved or
// not, so a rejection can be diagnosed from the numbers that caused it.
//
// The metric columns come from the shared imageResultRow, so this file and
// analyze's CSV cannot disagree on formatting; the session columns are
// prepended and the row's own filter/date are replaced by the source values.
func writeFramesCSV(targetOut string, sessions []sessionPlan) error {
	path := metricsPath(targetOut, framesCSVName)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to create %q: %w", path, err)
	}
	defer f.Close()

	w := newCSVWriter(f)
	if err := w.Write(framesCSVHeader); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}

	// imageResultRow is ordered as imageResultHeader: filename, filter, date,
	// then the metrics. Only the filename and the metrics are wanted here;
	// filter and date are replaced by the source subdirectory and session.
	const (
		colFilename = 0
		colFirst    = 3 // detectedStars onward
	)

	for _, s := range sessions {
		for _, filter := range s.filterNames() {
			for _, name := range s.filters[filter] {
				r, ok := s.verdicts[name]
				if !ok {
					// Should not happen: assessSessions records every planned
					// frame. Skip rather than emit a misleading empty row.
					continue
				}
				base := imageResultRow(r)

				row := make([]string, 0, len(framesCSVHeader))
				row = append(row,
					s.sessionDir(),
					s.date,
					"PA"+s.pa,
					filter,
					base[colFilename],
				)
				row = append(row, base[colFirst:]...)

				if len(row) != len(framesCSVHeader) {
					return fmt.Errorf("frames.csv: built %d columns, header has %d",
						len(row), len(framesCSVHeader))
				}
				if err := w.Write(row); err != nil {
					return fmt.Errorf("writing CSV: %w", err)
				}
			}
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}
	return nil
}

// writeSessionsCSV records the session index so the Session_NN numbering stays
// traceable back to the source dates and angles.
func writeSessionsCSV(targetOut string, sessions []sessionPlan) error {
	path := metricsPath(targetOut, sessionsCSVName)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("unable to create %q: %w", path, err)
	}
	defer f.Close()

	w := newCSVWriter(f)
	// gains and darks are appended last so the position of every existing
	// column is unchanged for anything already reading this file. darks holds
	// the gain/exposure pairs rather than bare exposures, because the pairing
	// is what identifies a master.
	header := []string{
		"session", "date", "pa", "filters", "lightCount",
		"approvedCount", "rejectedCount", "flatCount", "missingFlats",
		"frameDateMin", "frameDateMax", "gains", "darks",
	}
	if err := w.Write(header); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}

	for _, s := range sessions {
		filters := strings.Join(s.filterNames(), " ")
		missing := strings.Join(s.missingFlats, " ")
		darks := make([]string, 0, len(s.darksNeeded))
		for _, k := range s.darksNeeded {
			darks = append(darks, k.String())
		}
		// A camera without a rotator records no angle, so the column is empty
		// rather than a bare "PA". gains is likewise empty on the G4, which has
		// no gain setting; its bias is keyed by setpoint, which the darks column
		// already shows.
		pa := ""
		if s.pa != "" {
			pa = "PA" + s.pa
		}
		row := []string{
			s.sessionDir(),
			s.date,
			pa,
			filters,
			strconv.Itoa(s.lightCount),
			strconv.Itoa(s.approvedCount),
			strconv.Itoa(s.rejectedCount),
			strconv.Itoa(s.flatCount),
			missing,
			s.frameDateMin,
			s.frameDateMax,
			strings.Join(s.gains, " "),
			strings.Join(darks, " "),
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("writing CSV: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("writing CSV: %w", err)
	}
	return nil
}
