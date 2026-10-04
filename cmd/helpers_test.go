package cmd

import (
	"bytes"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/astrogo/fitsio"
)

// synthOpts controls the synthetic frame written by writeFrame.
type synthOpts struct {
	width, height int
	level         float64 // sky level
	noise         float64 // Gaussian read-noise sigma
	amp           float64 // star peak amplitude
	sigma         float64 // star Gaussian sigma in px
	spacing       int     // grid pitch; must exceed the star finder's skip box
	seed          int64
	// ratio stretches the PSF along y to make the field elliptical. 1 is
	// circular.
	ratio float64
}

// writeFrame builds a FITS frame containing a regular grid of Gaussian stars
// on a noisy sky and returns its path. Used to drive processImage with inputs
// whose true metrics are known by construction.
func writeFrame(t *testing.T, dir, name string, o synthOpts) string {
	t.Helper()

	if o.ratio == 0 {
		o.ratio = 1
	}
	rng := rand.New(rand.NewSource(o.seed))
	pix := make([]float32, o.width*o.height)
	for i := range pix {
		pix[i] = float32(o.level + rng.NormFloat64()*o.noise)
	}

	for cy := o.spacing + 5; cy < o.height-o.spacing-5; cy += o.spacing {
		for cx := o.spacing + 5; cx < o.width-o.spacing-5; cx += o.spacing {
			for dy := -8; dy <= 8; dy++ {
				for dx := -8; dx <= 8; dx++ {
					r2 := float64(dx*dx)/(2*o.sigma*o.sigma) +
						float64(dy*dy)/(2*o.sigma*o.sigma*o.ratio*o.ratio)
					pix[(cy+dy)*o.width+cx+dx] += float32(o.amp * math.Exp(-r2))
				}
			}
		}
	}

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer f.Close()

	tf, err := fitsio.Create(f)
	if err != nil {
		t.Fatalf("fitsio.Create: %v", err)
	}
	defer tf.Close()

	img := fitsio.NewImage(-32, []int{o.width, o.height})
	if err := img.Write(&pix); err != nil {
		t.Fatalf("write pixels: %v", err)
	}
	if err := tf.Write(img); err != nil {
		t.Fatalf("write HDU: %v", err)
	}
	return path
}

// frameName returns a filename following the instrument convention, so
// parseFilename extracts a filter and date.
func frameName(filter string, n int) string {
	return "LBN527_LIGHT_" + filter + "_300s_BIN1_-10C_GA0_20260101_000000_000_PA310_E" +
		string(rune('0'+n%10)) + ".FIT"
}

// defaultOpts mirrors the flag defaults registered in analyze.go's init(),
// sourcing the thresholds from the same place the commands do.
func defaultOpts() analyzeOptions {
	return analyzeOptions{
		qualityThresholds:  defaultThresholds,
		limitComputedStars: 500,
		format:             "console",
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written. runAnalyze prints with bare fmt.Printf, so there is no
// writer to inject.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stdout = orig
	out := <-done
	_ = r.Close()
	return out
}

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written. warnUnknownKeys reports with a bare fmt.Fprintf, so there
// is no writer to inject.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	_ = w.Close()
	os.Stderr = orig
	out := <-done
	_ = r.Close()
	return out
}

// Tests in this file must not call t.Parallel: processImage and runAnalyze read
// and write the package-level mrs.ConvWorkers global, so parallel execution
// would be a genuine data race under -race.
