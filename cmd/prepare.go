package cmd

import (
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
// staged on before calibration.
const (
	defaultPrepareInput  = "/Volumes/Dyno/Kosmodrom"
	defaultPrepareOutput = "/Volumes/T7/Workspace"
)

// lightsDir and flatsDir are the subdirectories of the input root holding
// science and flat frames respectively.
const (
	lightsDir = "Lights"
	flatsDir  = "Flats"
)

// sessionLightsDir and sessionFlatsDir are the subdirectories of a prepared
// session. Keeping lights and calibration symmetric means a downstream
// pipeline can treat the two alike.
const (
	sessionLightsDir = "lights"
	sessionFlatsDir  = "flats"
)

// rejectedDir sits beside the Sessions directory rather than inside a session,
// so a session tree holds only usable data and everything discarded is
// quarantined in one place.
const rejectedDir = "rejected"

// sessionsDir groups the prepared sessions, so the target root holds only
// Sessions/, rejected/ and metrics/.
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

// flatFilterRe extracts the filter from a flat frame filename, e.g.
// "FLAT_S_67.02s_...".
var flatFilterRe = regexp.MustCompile(`^FLAT_([A-Za-z0-9]+)_`)

// frameDateRe extracts the YYYYMMDD acquisition date embedded in a frame
// filename.
var frameDateRe = regexp.MustCompile(`_(\d{8})_\d{6}_\d+_PA`)

// fitsExts are the filename extensions treated as FITS frames.
var fitsExts = map[string]bool{".FIT": true, ".FITS": true, ".FTS": true}

type prepareOptions struct {
	// Quality thresholds are shared with analyze, so a light frame is approved
	// here on exactly the criteria analyze would apply.
	qualityThresholds

	target       string
	input        string
	output       string
	dryRun       bool
	skipExisting bool
	quiet        bool

	// workers and convWorkers control the quality pass; they mirror analyze's
	// flags and the mrs.ConvWorkers global.
	workers     int
	convWorkers int
}

var prepareOpts = prepareOptions{}

var prepareCmd = &cobra.Command{
	Use:   "prepare",
	Short: "Prépare les sessions d'une cible pour le traitement",
	Long: `Copie et réorganise les images d'une cible astronomerique
depuis l'arborescence d'acquisition vers une arborescence de
travail :

  <sortie>/<cible>/Session-NN/lights/<filtre>/*.FIT
  <sortie>/<cible>/Session-NN/flats/<filtre>/*.FIT
  <sortie>/<cible>/rejected/Session-NN/<filtre>/*.FIT

Une session correspond à un dossier de date dans Lights/<cible>.
Les numéros de session suivent l'ordre chronologique des dates.
Les flats sont sélectionnés par filtre et par position angle
(PA), sur la date de la session.

Chaque image est analysée avant copie : seules les images APPROUVÉE
sont placées dans lights/, les autres dans rejected/. Les seuils de
qualité sont ceux de la commande analyze.

Exemple :
  garfield prepare --target LBN527
  garfield prepare -t LBN527 -i /Volumes/Dyno/Kosmodrom -o /Volumes/T7/Workspace
  garfield prepare -t M51 --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPrepare(prepareOpts)
	},
}

func init() {
	prepareCmd.Flags().StringVarP(&prepareOpts.target, "target", "t", "", "cible astronomique (dossier dans Lights/)")
	prepareCmd.Flags().StringVarP(&prepareOpts.input, "input", "i", defaultPrepareInput, "racine des données d'acquisition")
	prepareCmd.Flags().StringVarP(&prepareOpts.output, "output", "o", defaultPrepareOutput, "racine de destination")
	prepareCmd.Flags().BoolVar(&prepareOpts.dryRun, "dry-run", false, "affiche le plan sans copier")
	prepareCmd.Flags().BoolVar(&prepareOpts.skipExisting, "skip-existing", false, "ne remplace pas les fichiers existants")
	prepareCmd.Flags().BoolVar(&prepareOpts.quiet, "quiet", false, "évite la sortie console")

	registerQualityFlags(prepareCmd.Flags(), &prepareOpts.qualityThresholds)

	registerWorkerFlags(prepareCmd.Flags(), &prepareOpts.workers, &prepareOpts.convWorkers)
}

// sessionPlan is one observing night to prepare.
type sessionPlan struct {
	number int    // 1-based, drives the Session-NN directory name
	date   string // YYYY-MM-DD, the Lights source directory name
	pa     string // position angle shared by the session, digits only
	// filters holds the light filenames per filter, keyed by the Lights
	// filter subdirectory name.
	filters map[string][]string
	// flats holds the matching flat filenames per filter.
	flats map[string][]string
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
	lightCount   int
	flatCount    int
	// approvedCount and rejectedCount are filled in by the assessment pass.
	approvedCount int
	rejectedCount int
	// rejectReasons counts frames per decision, for the console summary.
	rejectReasons map[string]int
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

// sessionDir returns the output directory name for the session.
func (s sessionPlan) sessionDir() string {
	return fmt.Sprintf("Session-%02d", s.number)
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
		return fmt.Errorf("--target est requis")
	}
	if opts.input == "" {
		return fmt.Errorf("--input ne peut pas être vide")
	}
	if opts.output == "" {
		return fmt.Errorf("--output ne peut pas être vide")
	}

	lightsRoot := filepath.Join(opts.input, lightsDir, opts.target)
	info, err := os.Stat(lightsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cible %q introuvable dans %q", opts.target, filepath.Join(opts.input, lightsDir))
		}
		return fmt.Errorf("impossible d'accéder à %q : %w", lightsRoot, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q n'est pas un dossier", lightsRoot)
	}

	targetOut := filepath.Join(opts.output, opts.target)

	sessions, warnings, err := planSessions(lightsRoot, filepath.Join(opts.input, flatsDir))
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return fmt.Errorf("aucune session (dossier YYYY-MM-DD) trouvée dans %q", lightsRoot)
	}

	if !opts.quiet {
		fmt.Printf("\n%s de %q (%d session(s)) vers %q%s\n\n",
			prepareVerb(opts.dryRun), opts.target, len(sessions), targetOut, dryRunSuffix(opts.dryRun))
		if !opts.dryRun {
			fmt.Printf("Analyse de la qualité de %d image(s), cela peut prendre plusieurs minutes...\n\n",
				totalLightCount(sessions))
		}
	}

	// Assess every light frame before copying anything, so the destination
	// tree reflects the final decision for each frame.
	if err := assessSessions(sessions, opts); err != nil {
		return err
	}

	if !opts.dryRun {
		if err := os.MkdirAll(targetOut, 0o755); err != nil {
			return fmt.Errorf("impossible de créer %q : %w", targetOut, err)
		}
		if err := os.MkdirAll(filepath.Join(targetOut, metricsDir), 0o755); err != nil {
			return fmt.Errorf("impossible de créer %q : %w", filepath.Join(targetOut, metricsDir), err)
		}
	}

	totalLights, totalFlats := 0, 0
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
			detail += fmt.Sprintf(" : %d approuvé(s), %d rejeté(s) [%s]",
				s.approvedCount, s.rejectedCount, strings.Join(s.filterNames(), " "))
			if copiedFlats > 0 {
				detail += fmt.Sprintf(", %d flat(s)", copiedFlats)
			} else if len(s.missingFlats) > 0 {
				detail += fmt.Sprintf(", aucun flat pour [%s]", strings.Join(s.missingFlats, " "))
			}
			if opts.dryRun {
				detail = "(simulation) " + detail
			}
			fmt.Println("  " + detail)

			if reason := s.dominantRejectReason(); reason != "" {
				fmt.Printf("      motif de rejet : %s\n", reason)
			}
		}
	}

	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  avertissement : %s\n", w)
	}

	approved, rejected := 0, 0
	for _, s := range sessions {
		approved += s.approvedCount
		rejected += s.rejectedCount
	}

	if !opts.quiet {
		fmt.Printf("\n%s : %d session(s), %d image(s) approuvée(s), %d rejetée(s), %d flat(s).\n",
			finishVerb(opts.dryRun), len(sessions), approved, rejected, totalFlats)
		if rejected > 0 && !opts.dryRun {
			fmt.Printf("Images rejetées sous %q.\n", filepath.Join(targetOut, rejectedDir))
		}
		if !opts.dryRun {
			fmt.Printf("Index écrit dans %q.\n", metricsPath(targetOut, sessionsCSVName))
			fmt.Printf("Métriques par image écrites dans %q.\n", metricsPath(targetOut, framesCSVName))
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

func dryRunSuffix(dry bool) string {
	if dry {
		return " [simulation]"
	}
	return ""
}

func finishVerb(dry bool) string {
	if dry {
		return "Simulation terminée"
	}
	return "Préparation terminée"
}

func prepareVerb(dry bool) string {
	if dry {
		return "Simulation"
	}
	return "Préparation"
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

// planSessions walks the Lights target directory and builds a plan per session,
// pairing each with the flats of the same date, filter and position angle.
func planSessions(lightsRoot, flatsRoot string) ([]sessionPlan, []string, error) {
	entries, err := os.ReadDir(lightsRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("impossible de lire %q : %w", lightsRoot, err)
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
	)

	for i, date := range dates {
		s, warns, err := planSession(filepath.Join(lightsRoot, date), filepath.Join(flatsRoot, date), i+1, date)
		if err != nil {
			return nil, nil, err
		}
		sessions = append(sessions, s)
		warnings = append(warnings, warns...)
	}

	return sessions, warnings, nil
}

// planSession builds the plan for a single session directory.
func planSession(sessionDir, flatDir string, number int, date string) (sessionPlan, []string, error) {
	s := sessionPlan{
		number:  number,
		date:    date,
		filters: map[string][]string{},
		flats:   map[string][]string{},
	}

	filterDirs, err := filterSubdirs(sessionDir)
	if err != nil {
		return s, nil, err
	}

	var warnings []string
	paSeen := map[string]bool{}

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
			if m := paRe.FindStringSubmatch(f); m != nil {
				paSeen[m[1]] = true
			}
		}
	}

	if len(paSeen) > 0 {
		angles := make([]string, 0, len(paSeen))
		for a := range paSeen {
			angles = append(angles, a)
		}
		sort.Strings(angles)
		s.pa = angles[0]
		if len(angles) > 1 {
			warnings = append(warnings, fmt.Sprintf(
				"session %s (%s) : angles multiples %s, flats pris pour PA%s",
				s.sessionDir(), date, strings.Join(angles, "/"), s.pa))
		}
	}

	// Flats are matched on the session date, not the date embedded in the
	// light filenames: sessions run past midnight, so the two disagree for
	// most nights and only the directory name lines up with Flats/.
	flatIndex := indexFlats(flatDir)

	for _, filter := range s.filterNames() {
		key := flatKey(filter, s.pa)
		matched := flatIndex[key]
		if len(matched) == 0 {
			s.missingFlats = append(s.missingFlats, filter)
			warnings = append(warnings, fmt.Sprintf(
				"session %s (%s) : aucun flat pour le filtre %s (PA%s) dans %q",
				s.sessionDir(), date, filter, s.pa, flatDir))
			continue
		}
		s.flats[filter] = matched
		s.flatCount += len(matched)
	}

	return s, warnings, nil
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

func filterSubdirs(sessionDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("impossible de lire %q : %w", sessionDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// flatKey identifies a flat set by filter and position angle.
func flatKey(filter, pa string) string {
	return filter + "@" + pa
}

// indexFlats groups the flat frames of one date by filter and angle.
func indexFlats(flatDir string) map[string][]string {
	index := map[string][]string{}

	entries, err := os.ReadDir(flatDir)
	if err != nil {
		// A missing or unreadable flats directory simply yields no flats; the
		// caller reports the gap per session.
		return index
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isFITSName(name) {
			continue
		}
		mf := flatFilterRe.FindStringSubmatch(name)
		if mf == nil {
			continue
		}
		mp := paRe.FindStringSubmatch(name)
		if mp == nil {
			continue
		}
		key := flatKey(mf[1], mp[1])
		index[key] = append(index[key], name)
	}

	for k := range index {
		sort.Strings(index[k])
	}
	return index
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
		return nil, fmt.Errorf("impossible de lire %q : %w", dir, err)
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
					return lights, flats, fmt.Errorf("impossible de créer %q : %w", filepath.Dir(dst), err)
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
		srcDir := filepath.Join(opts.input, flatsDir, s.date)
		for _, name := range matched {
			dst := filepath.Join(flatRoot, filter, name)
			if !opts.dryRun {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return lights, flats, fmt.Errorf("impossible de créer %q : %w", filepath.Dir(dst), err)
				}
			}
			n, err := copyFITS(filepath.Join(srcDir, name), dst, opts)
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
		return false, fmt.Errorf("impossible d'ouvrir %q : %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return false, fmt.Errorf("impossible de créer %q : %w", dst, err)
	}

	buf := make([]byte, copyBufferSize)
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		out.Close()
		return false, fmt.Errorf("échec de la copie %q -> %q : %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return false, fmt.Errorf("fermeture de %q : %w", dst, err)
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
		return fmt.Errorf("impossible de créer %q : %w", path, err)
	}
	defer f.Close()

	w := newCSVWriter(f)
	if err := w.Write(framesCSVHeader); err != nil {
		return fmt.Errorf("écriture CSV : %w", err)
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
					return fmt.Errorf("écriture CSV : %w", err)
				}
			}
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("écriture CSV : %w", err)
	}
	return nil
}

// writeSessionsCSV records the session index so the Session-NN numbering stays
// traceable back to the source dates and angles.
func writeSessionsCSV(targetOut string, sessions []sessionPlan) error {
	path := metricsPath(targetOut, sessionsCSVName)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("impossible de créer %q : %w", path, err)
	}
	defer f.Close()

	w := newCSVWriter(f)
	header := []string{
		"session", "date", "pa", "filters", "lightCount",
		"approvedCount", "rejectedCount", "flatCount", "missingFlats",
		"frameDateMin", "frameDateMax",
	}
	if err := w.Write(header); err != nil {
		return fmt.Errorf("écriture CSV : %w", err)
	}

	for _, s := range sessions {
		filters := strings.Join(s.filterNames(), " ")
		missing := strings.Join(s.missingFlats, " ")
		row := []string{
			s.sessionDir(),
			s.date,
			"PA" + s.pa,
			filters,
			strconv.Itoa(s.lightCount),
			strconv.Itoa(s.approvedCount),
			strconv.Itoa(s.rejectedCount),
			strconv.Itoa(s.flatCount),
			missing,
			s.frameDateMin,
			s.frameDateMax,
		}
		if err := w.Write(row); err != nil {
			return fmt.Errorf("écriture CSV : %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("écriture CSV : %w", err)
	}
	return nil
}
