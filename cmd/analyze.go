package cmd

import (
	"encoding/csv"
	"fmt"
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
	dir                string
	workers            int
	convWorkers        int
	minSNR             float64
	maxFWHM            float64
	maxEcc             float64
	minScore           float64
	minStars           int
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
	analyzeCmd.Flags().IntVarP(&analyzeOpts.workers, "workers", "w", 0, "workers externes (0 = auto : NumCPU/2)")
	analyzeCmd.Flags().IntVar(&analyzeOpts.convWorkers, "conv-workers", 0, "workers de convolution internes (0 = auto)")
	analyzeCmd.Flags().Float64Var(&analyzeOpts.minSNR, "min-snr", 11.0, "SNR minimal pour APPROUVÉE")
	analyzeCmd.Flags().Float64Var(&analyzeOpts.maxFWHM, "max-fwhm", 5.0, "FWHM maximale pour APPROUVÉE")
	analyzeCmd.Flags().Float64Var(&analyzeOpts.maxEcc, "max-ecc", 0.50, "excentricité maximale pour APPROUVÉE")
	analyzeCmd.Flags().Float64Var(&analyzeOpts.minScore, "min-score", 2.0, "score minimal pour APPROUVÉE")
	analyzeCmd.Flags().IntVar(&analyzeOpts.minStars, "min-stars", 680, "nombre mminimum d'étoiles détectée pour APPROUVÉE")
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
	// is already under minUsableStars, and guard sparse fields from
	// degenerate 0/Inf metrics.
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
			Decision:        "REJETÉE MANQUE ÉTOILES",
		}
	}

	var sumFlux, sumWeight float64
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
		sumFlux += fieldStars[i].TotalFlux
		sumWeight += w
		filteredCount++
	}

	avgFWHM := weightedFWHM / sumWeight
	avgEcc := weightedEcc / sumWeight
	avgPeakSignal := totalPeakSignal / float64(filteredCount)

	snrLinear := mrsResult.ImageSNR
	finalScore := (snrLinear / avgFWHM) * (1.0 - avgEcc)

	decision := "APPROUVEE"
	if len(fieldStars) < opts.minStars {
		decision = "REJETEE MANQUE ETOILES"
	} else if avgEcc > opts.maxEcc {
		decision = "REJETEE EXCENTRICITY"
	} else if avgFWHM > opts.maxFWHM {
		decision = "REJETEE FWHM"
	} else if snrLinear < opts.minSNR {
		decision = "REJETEE SNR"
	} else if finalScore < opts.minScore {
		decision = "REJETEE SCORING"
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

func writeResultsCSV(results []ImageResult, w *csv.Writer) error {
	header := []string{"filename", "filter", "date", "detectedStars", "starCount", "avgFWHM", "avgSignal", "avgEccentricity", "snr", "score", "decision", "error"}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, r := range results {
		errStr := ""
		if r.Error != nil {
			errStr = r.Error.Error()
		}
		row := []string{
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
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
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

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("impossible de lire le dossier %q : %w", dir, err)
	}

	var fitsFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToUpper(filepath.Ext(e.Name()))
		if ext == ".FIT" || ext == ".FITS" || ext == ".FTS" {
			fitsFiles = append(fitsFiles, filepath.Join(dir, e.Name()))
		}
	}

	sort.Strings(fitsFiles)

	if len(fitsFiles) == 0 {
		return fmt.Errorf("aucun fichier FITS trouvé dans %q", dir)
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
						fmt.Printf("  [%d/%d] %s ... OK (%d/%d étoiles, FWHM=%.2f, Ecc=%.3f, SNR=%.2f, %s)\n",
							done, len(fitsFiles), r.Filename, r.StarCount, r.DetectedStars, r.AvgFWHM, r.AvgEccentricity, r.SNR, r.Decision)
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
			w := csv.NewWriter(os.Stdout)
			if err := writeResultsCSV(results, w); err != nil {
				return fmt.Errorf("écriture CSV : %w", err)
			}
		} else {
			f, err := os.Create(opts.output)
			if err != nil {
				return fmt.Errorf("impossible de créer le fichier CSV %q : %w", opts.output, err)
			}
			w := csv.NewWriter(f)
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
