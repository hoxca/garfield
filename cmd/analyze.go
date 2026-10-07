package cmd

import (
	"encoding/csv"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"garfield/internal/mrs"
	"garfield/internal/readfits"
	"garfield/internal/starsmetrics"

	"github.com/spf13/cobra"
)

type analyzeOptions struct {
	qualityThresholds

	dir string
	// recursive extends the search to subdirectories of dir. Off by default, so
	// pointing analyze at a directory containing frames analyses exactly those.
	recursive          bool
	workers            int
	convWorkers        int
	limitComputedStars int
	format             string
	output             string
	quiet              bool
}

var analyzeOpts = analyzeOptions{}

var analyzeCmd = &cobra.Command{
	Use:     "analyze [dir]",
	Aliases: []string{"analyse"},
	Short:   "Analyse un dossier d'images FITS",
	Long: `Parcourt un dossier, détecte les fichiers .fit/.fits/.fts
et calcule FWHM, excentricité, SNR et score pour chacun.

Exemple :
  garfield analyze images/
  garfield analyze --dir images/ --limit-computed-stars 500
  garfield analyze --dir images/ --format csv --output resultats.csv
  garfield analyze --dir images/ --format both --output resultats.csv --quiet`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := analyzeOpts.dir
		if len(args) == 1 {
			dir = args[0]
		}
		return runAnalyze(dir, analyzeOpts)
	},
}

func init() {
	analyzeCmd.Flags().StringVarP(&analyzeOpts.dir, "dir", "d", "images", "dossier contenant les FITS")
	analyzeCmd.Flags().BoolVarP(&analyzeOpts.recursive, "recursive", "r", false,
		fmt.Sprintf("parcourt les sous-dossiers de --dir, sur %d niveaux", maxRecursiveDepth))
	registerWorkerFlags(analyzeCmd.Flags(), &analyzeOpts.workers, &analyzeOpts.convWorkers)
	registerQualityFlags(analyzeCmd.Flags(), &analyzeOpts.qualityThresholds)
	analyzeCmd.Flags().IntVar(&analyzeOpts.limitComputedStars, "limit-computed-stars", 500, "limite le nombre d'étoiles brillantes analysées")
	analyzeCmd.Flags().StringVar(&analyzeOpts.format, "format", "console", "format de sortie : console, csv ou both")
	analyzeCmd.Flags().StringVarP(&analyzeOpts.output, "output", "o", "", "fichier CSV de sortie (requis pour csv/both vers fichier)")
	analyzeCmd.Flags().BoolVar(&analyzeOpts.quiet, "quiet", false, "évite la sortie console")
}

// ImageResult holds the quality metrics for one FITS file.
type ImageResult struct {
	Filename        string
	Filter          string
	Date            string
	DetectedStars   int
	StarCount       int
	AvgFWHM         float64
	AvgSignal       float64
	AvgEccentricity float64
	SNR             float64
	Score           float64
	Decision        string
	Error           error
}

const (
	decisionApproved = "APPROUVEE"
	decisionNoStars  = "REJETEE MANQUE ETOILES"
	decisionEcc      = "REJETEE EXCENTRICITY"
	decisionFWHM     = "REJETEE FWHM"
	decisionSNR      = "REJETEE SNR"
	decisionScoring  = "REJETEE SCORING"
)

func parseFilename(name string) (filter string, date string) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.Split(base, "_")
	if len(parts) >= 3 {
		filter = parts[2]
	}
	if len(parts) >= 8 {
		raw := parts[7]
		if len(raw) >= 8 {
			date = raw[:4] + "-" + raw[4:6] + "-" + raw[6:8]
		}
	}
	return
}

func processImage(path string, opts analyzeOptions) ImageResult {
	name := filepath.Base(path)
	filter, date := parseFilename(name)

	pixels, err := readfits.ReadFITSImage(path)
	if err != nil {
		return ImageResult{Filename: name, Filter: filter, Date: date, Error: err}
	}

	scales := 5
	mrsResult := mrs.EstimateBackgroundAndNoiseBinned(pixels, scales)
	bgMap := mrsResult.Background
	skyNoise := mrsResult.NoiseSigma

	fieldStars := starsmetrics.FindStars(pixels, bgMap, skyNoise)

	sort.Slice(fieldStars, func(i, j int) bool {
		return fieldStars[i].TotalFlux > fieldStars[j].TotalFlux
	})

	if len(fieldStars) == 0 {
		return ImageResult{
			Filename: name, Filter: filter, Date: date,
			Error: fmt.Errorf("aucune étoile trouvée"),
		}
	}

	targetCount := opts.limitComputedStars
	if len(fieldStars) < targetCount {
		targetCount = len(fieldStars)
	}

	// Fixed skip of the brightest stars (saturated / non-representative).
	// Skip expensive per-star metrics (eccentricity) when the usable pool
	// is already under minUsableStars, and guard sparse fields from degenerate 0/Inf metrics.
	const brightSkip = 20
	const minUsableStars = 80

	if targetCount-brightSkip < minUsableStars {
		return ImageResult{
			Filename:        name,
			Filter:          filter,
			Date:            date,
			DetectedStars:   len(fieldStars),
			StarCount:       0,
			AvgFWHM:         0,
			AvgSignal:       0,
			AvgEccentricity: 0,
			SNR:             mrsResult.ImageSNR,
			Score:           0,
			Decision:        decisionNoStars,
		}
	}

	var sumWeight float64
	var weightedFWHM, weightedEcc float64
	var filteredCount int
	var totalPeakSignal float64

	for i := brightSkip; i < targetCount; i++ {
		if fieldStars[i].Signal < 50.0*skyNoise {
			continue
		}
		fwhm := fieldStars[i].FWHM
		ecc, okEcc := starsmetrics.CalculateEccentricity(pixels, bgMap, fieldStars[i].X, fieldStars[i].Y, 10)
		if !okEcc || math.IsNaN(ecc) {
			continue
		}
		w := fieldStars[i].TotalFlux
		weightedFWHM += fwhm * w
		weightedEcc += ecc * w
		totalPeakSignal += fieldStars[i].Signal
		sumWeight += w
		filteredCount++
	}

	// Every star failed the per-star filters (peak signal below the noise
	// floor, or degenerate eccentricity). The metric averages below would divide 0/0 and produce NaN, and since all NaN comparisons are false
	// that NaN would silently bypass the FWHM/eccentricity/scoring gates and mark the frame APPROUVEE. Reject before computing any metric.
	if filteredCount == 0 || sumWeight <= 0 {
		return ImageResult{
			Filename:      name,
			Filter:        filter,
			Date:          date,
			DetectedStars: len(fieldStars),
			StarCount:     0,
			SNR:           mrsResult.ImageSNR,
			Decision:      decisionNoStars,
		}
	}

	avgFWHM := weightedFWHM / sumWeight
	avgEcc := weightedEcc / sumWeight
	avgPeakSignal := totalPeakSignal / float64(filteredCount)

	snrLinear := mrsResult.ImageSNR
	finalScore := (snrLinear / avgFWHM) * (1.0 - avgEcc)

	decision := decisionApproved
	if len(fieldStars) < opts.minStars {
		decision = decisionNoStars
	} else if avgEcc > opts.maxEcc {
		decision = decisionEcc
	} else if avgFWHM > opts.maxFWHM {
		decision = decisionFWHM
	} else if snrLinear < opts.minSNR {
		decision = decisionSNR
	} else if finalScore < opts.minScore {
		decision = decisionScoring
	}

	return ImageResult{
		Filename:        name,
		Filter:          filter,
		Date:            date,
		DetectedStars:   len(fieldStars),
		StarCount:       filteredCount,
		AvgFWHM:         avgFWHM,
		AvgSignal:       avgPeakSignal,
		AvgEccentricity: avgEcc,
		SNR:             snrLinear,
		Score:           finalScore,
		Decision:        decision,
	}
}

// csvSeparator matches the convention used by the reference tool's report
// (data/LBN527_result.csv), so garfield output can be read alongside it
// directly.
const csvSeparator = ';'

// newCSVWriter returns a CSV writer using garfield's separator. Every writer in
// the tool goes through here so the convention cannot drift between commands.
func newCSVWriter(w io.Writer) *csv.Writer {
	cw := csv.NewWriter(w)
	cw.Comma = csvSeparator
	return cw
}

// newCSVReader returns a CSV reader for garfield's output. Kept beside
// newCSVWriter so the two stay in step.
func newCSVReader(r io.Reader) *csv.Reader {
	cr := csv.NewReader(r)
	cr.Comma = csvSeparator
	return cr
}

// imageResultHeader names the metric columns shared by analyze's CSV and
// prepare's frames.csv.
var imageResultHeader = []string{"filename", "filter", "date", "detectedStars", "starCount", "avgFWHM", "avgSignal", "avgEccentricity", "snr", "score", "decision", "error"}

// imageResultRow renders one result as imageResultHeader columns. Both
// commands go through here so their metric columns and number formatting cannot
// drift apart.
func imageResultRow(r ImageResult) []string {
	errStr := ""
	if r.Error != nil {
		errStr = r.Error.Error()
	}
	return []string{
		r.Filename,
		r.Filter,
		r.Date,
		strconv.Itoa(r.DetectedStars),
		strconv.Itoa(r.StarCount),
		strconv.FormatFloat(r.AvgFWHM, 'f', 4, 64),
		strconv.FormatFloat(r.AvgSignal, 'f', 4, 64),
		strconv.FormatFloat(r.AvgEccentricity, 'f', 4, 64),
		strconv.FormatFloat(r.SNR, 'f', 4, 64),
		strconv.FormatFloat(r.Score, 'f', 4, 64),
		r.Decision,
		errStr,
	}
}

func writeResultsCSV(results []ImageResult, w *csv.Writer) error {
	if err := w.Write(imageResultHeader); err != nil {
		return err
	}
	for _, r := range results {
		if err := w.Write(imageResultRow(r)); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// maxRecursiveDepth is how many levels of subdirectory -r descends. The
// acquisition stores a frame as <target>/<YYYY-MM-DD>/<FILTER>/<frame>.FIT, two
// levels below Lights/<target>, and no frame anywhere in the tree sits deeper --
// every one of the sixteen targets measures exactly two. So this covers a target
// in full, while stopping the scan from wandering into an unrelated subtree.
const maxRecursiveDepth = 2

// discoverFITSFiles collects the FITS frames in dir, descending into
// subdirectories only when recursive is set. The default is deliberately flat:
// analysing a directory should mean the frames in it, and reaching into the tree
// is opt-in so a stray frame below the chosen directory cannot quietly join the
// report. A recursive scan stops at maxRecursiveDepth and says so when it does.
//
// Dotfiles are skipped in both modes, through isFITSName. macOS writes
// AppleDouble sidecars named "._<frame>.FIT" beside every frame on an exFAT
// volume, and they carry a FITS extension, so without this they would all be
// analysed and each would fail as unreadable, filling the report with error
// rows. They appear at every level, not only in subdirectories, so the flat scan
// needs the rule too. prepare applies the same one.
func discoverFITSFiles(dir string, recursive bool) ([]string, bool, error) {
	if recursive {
		return walkFITSFiles(dir)
	}
	fitsFiles, err := topLevelFITSFiles(dir)
	return fitsFiles, false, err
}

// topLevelFITSFiles collects the frames sitting directly in dir.
func topLevelFITSFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("impossible de lire le dossier %q : %w", dir, err)
	}

	var fitsFiles []string
	for _, e := range entries {
		// A directory named like a frame is not a frame.
		if e.IsDir() || !isFITSName(e.Name()) {
			continue
		}
		fitsFiles = append(fitsFiles, filepath.Join(dir, e.Name()))
	}
	return fitsFiles, nil
}

// walkFITSFiles collects the frames in dir and its subdirectories, down to
// maxRecursiveDepth levels. dirLevel measures the scan root as 0, so a frame in
// dir/a/b is at depth 2 and one in dir/a/b/c at depth 3.
//
// Directories are cut on their own depth rather than on the frames inside them:
// the directory at depth 3 is skipped whole, which keeps its depth-3 frames out
// while leaving the depth-2 frames of dir/a/b collected.
//
// Two further exclusions. Directories named like a frame are never collected,
// which covers the trap of a directory called "trap.FIT". Symlinked directories
// are not descended, matching filepath.WalkDir: following them risks cycles, and
// the acquisition volumes hold none. A symlink to a frame file is still
// analysed; only directories are left alone.
//
// The second return value reports whether the depth cap stopped the walk, so the
// caller can say the search was bounded rather than complete.
func walkFITSFiles(dir string) ([]string, bool, error) {
	var (
		fitsFiles    []string
		wasTruncated bool
	)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && dirLevel(dir, path) > maxRecursiveDepth {
				wasTruncated = true
				return fs.SkipDir
			}
			return nil
		}
		if !isFITSName(d.Name()) {
			return nil
		}
		fitsFiles = append(fitsFiles, path)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("impossible de lire le dossier %q : %w", dir, err)
	}
	return fitsFiles, wasTruncated, nil
}

// dirLevel counts the subdirectory levels between the scan root and path, the
// root itself being 0.
func dirLevel(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// warnDuplicateBasenames reports frames whose names collide across directories.
// Only meaningful for a recursive scan, which is the only one that can reach the
// same basename twice.
//
// The report records the bare filename, so two frames called the same thing in
// different subdirectories produce two indistinguishable rows. Real acquisition
// names embed target, filter, date, time and position angle and so never
// collide, but a hand-assembled tree can, and the ambiguity is worth surfacing
// rather than leaving to be noticed later.
func warnDuplicateBasenames(fitsFiles []string) []string {
	seen := map[string]bool{}
	dupes := map[string]bool{}

	for _, path := range fitsFiles {
		base := filepath.Base(path)
		if seen[base] {
			dupes[base] = true
			continue
		}
		seen[base] = true
	}

	names := make([]string, 0, len(dupes))
	for n := range dupes {
		names = append(names, n)
	}
	sort.Strings(names)

	warnings := make([]string, 0, len(names))
	for _, n := range names {
		warnings = append(warnings, fmt.Sprintf(
			"plusieurs images portent le nom %q dans des sous-dossiers différents ; "+
				"le rapport ne les distingue que par leur position", n))
	}
	return warnings
}

func runAnalyze(dir string, opts analyzeOptions) error {
	switch opts.format {
	case "console", "csv", "both":
	default:
		return fmt.Errorf("format invalide %q : attendu console, csv ou both", opts.format)
	}
	if opts.format == "both" && opts.output == "" {
		return fmt.Errorf("--output est requis avec --format both")
	}

	fitsFiles, truncated, err := discoverFITSFiles(dir, opts.recursive)
	if err != nil {
		return err
	}

	// WalkDir visits each directory in lexical order, but that is per directory,
	// so a/z.FIT is still reached before b/a.FIT. Sorting the full paths is what
	// makes CSV rows come out alphabetically rather than in completion order,
	// which is otherwise nondeterministic across runs. The flat scan reads a
	// single directory, so this only restores that ordering for it.
	sort.Strings(fitsFiles)

	if len(fitsFiles) == 0 {
		// The flat case is where someone pointed at the wrong level of a tree,
		// so it names the flag rather than just reporting an empty directory.
		if opts.recursive {
			// Not "ni dans ses sous-dossiers": with a depth cap that would be
			// false, since frames can sit below what was searched.
			return fmt.Errorf(
				"aucun fichier FITS trouvé dans %q dans les %d niveaux de sous-dossiers",
				dir, maxRecursiveDepth)
		}
		return fmt.Errorf(
			"aucun fichier FITS trouvé dans %q ; utilisez -r pour parcourir aussi les sous-dossiers", dir)
	}

	// Said whenever the cap actually cut the walk short, so a bounded search is
	// never mistaken for a complete one.
	if truncated {
		fmt.Fprintf(os.Stderr,
			"  note : recherche récursive limitée à %d niveaux ; des sous-dossiers plus profonds n'ont pas été parcourus\n",
			maxRecursiveDepth)
	}

	// Outer workers scale with CPUs; inner convolution workers are divided
	// accordingly so total threads stay close to NumCPU instead of
	// oversubscribing (outer x NumCPU as before).
	numCPU := runtime.NumCPU()
	outerWorkers := opts.workers
	if outerWorkers <= 0 {
		outerWorkers = numCPU / 2
		if outerWorkers < 2 {
			outerWorkers = 2
		}
	}
	if outerWorkers > len(fitsFiles) {
		outerWorkers = len(fitsFiles)
	}
	convWorkers := opts.convWorkers
	if convWorkers <= 0 {
		convWorkers = numCPU / outerWorkers
		if convWorkers < 1 {
			convWorkers = 1
		}
	}
	mrs.ConvWorkers = convWorkers

	// Only reachable when recursing: within a single directory names are
	// unique, so a flat scan cannot produce a collision.
	if opts.recursive {
		for _, w := range warnDuplicateBasenames(fitsFiles) {
			fmt.Fprintf(os.Stderr, "  avertissement : %s\n", w)
		}
	}

	showConsole := opts.format == "console" || opts.format == "both"
	csvToStdout := opts.format == "csv" && opts.output == ""
	if !opts.quiet && showConsole {
		fmt.Printf("\nTraitement de %d fichier(s) FITS dans %q (%d workers, conv=%d)...\n\n", len(fitsFiles), dir, outerWorkers, convWorkers)
	}

	results := make([]ImageResult, len(fitsFiles))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var printMu sync.Mutex
	done := 0

	for w := 0; w < outerWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				r := processImage(fitsFiles[idx], opts)
				// Distinct indices: no lock needed for the write.
				results[idx] = r
				printMu.Lock()
				done++
				if !opts.quiet && showConsole {
					if r.Error != nil {
						fmt.Printf("  [%d/%d] %s ... ERREUR: %v\n", done, len(fitsFiles), r.Filename, r.Error)
					} else {
						// Score is printed to three decimals, like Ecc, because the
						// score gate is the finest of the six: a frame sitting just
						// above --min-score would render as exactly on it at two.
						fmt.Printf("  [%d/%d] %s ... OK (%d/%d étoiles, FWHM=%.2f, Ecc=%.3f, SNR=%.2f, Score=%.3f, %s)\n",
							done, len(fitsFiles), r.Filename, r.StarCount, r.DetectedStars,
							r.AvgFWHM, r.AvgEccentricity, r.SNR, r.Score, r.Decision)
					}
				}
				printMu.Unlock()
			}
		}()
	}
	for i := range fitsFiles {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	if opts.format == "csv" || opts.format == "both" {
		if csvToStdout {
			w := newCSVWriter(os.Stdout)
			if err := writeResultsCSV(results, w); err != nil {
				return fmt.Errorf("écriture CSV : %w", err)
			}
		} else {
			f, err := os.Create(opts.output)
			if err != nil {
				return fmt.Errorf("impossible de créer le fichier CSV %q : %w", opts.output, err)
			}
			w := newCSVWriter(f)
			writeErr := writeResultsCSV(results, w)
			closeErr := f.Close()
			if writeErr != nil {
				return fmt.Errorf("écriture CSV : %w", writeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("fermeture CSV : %w", closeErr)
			}
			if !opts.quiet {
				fmt.Printf("Résultats CSV écrits dans %q (%d lignes).\n", opts.output, len(results))
			}
		}
	}

	if !opts.quiet && showConsole {
		fmt.Printf("\nTerminé : %d fichier(s) traité(s).\n", len(results))
	}
	return nil
}
