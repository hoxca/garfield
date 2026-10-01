// Package mrs compute snr with multiscale resolution support //
package mrs

import (
	"math"
	"runtime"
	"sync"
)

/*
B3SplineFilter values are specific to the exact B3-spline kernel [1/16, 4/16, 6/16, 4/16, 1/16] and the convolution boundary handling used.
*/
var B3SplineFilter = []float64{1.0 / 16.0, 4.0 / 16.0, 6.0 / 16.0, 4.0 / 16.0, 1.0 / 16.0}

/*
ScaleNoiseFactors values come from Starck & Murtagh (1998) and are computed as the norm of the equivalent filter kernel at each scale:
- Scale 1 (finest): 0.89079 — noise passes through ~89% at the finest scale
- Scale 2: 0.20066
- Scale 3: 0.08556
- Scale 4: 0.04123
- Scale 5 (coarsest): 0.02042 — noise is strongly suppressed at coarse scales
they are specific to the B3-spline kernel and the convolution boundary used in this algorythm
*/
var ScaleNoiseFactors = []float64{0.89079, 0.20066, 0.08556, 0.04123, 0.02042}

type ATrousLayerFlat struct {
	Smooth []float64
	Detail []float64
}

// ConvWorkers caps inner convolution parallelism. The main program lowers it
// when several images are processed concurrently so total threads stay close
// to runtime.NumCPU() instead of oversubscribing (outer workers x NumCPU).
var ConvWorkers = runtime.NumCPU()

type MRSResult struct {
	Background [][]float64
	NoiseSigma float64
	ImageSNR   float64
}

func toFlat(image [][]float64) ([]float64, int, int) {
	height := len(image)
	width := len(image[0])
	flat := make([]float64, height*width)
	for i := 0; i < height; i++ {
		copy(flat[i*width:(i+1)*width], image[i])
	}
	return flat, width, height
}

func to2D(flat []float64, width, height int) [][]float64 {
	out := make([][]float64, height)
	for i := 0; i < height; i++ {
		out[i] = flat[i*width : (i+1)*width]
	}
	return out
}

func EstimateBackgroundAndNoise(image [][]float64, scales int) MRSResult {
	kSigma := 3.0

	flat, w, h := toFlat(image)
	layers := ATrousTransformFlat(flat, w, h, scales)
	noiseSigma := EstimateNoiseSigmaFlat(layers[0].Detail, w, h, ScaleNoiseFactors[0])

	total := w * h
	support := make([]int, total)
	for s := 0; s < scales-1; s++ {
		scaleThreshold := kSigma * noiseSigma * ScaleNoiseFactors[s]
		detail := layers[s].Detail
		for i := 0; i < total; i++ {
			if support[i] == 0 && math.Abs(detail[i]) > scaleThreshold {
				support[i] = 1
			}
		}
	}

	bg := make([]float64, total)
	copy(bg, layers[scales-1].Smooth)
	for s := scales - 2; s >= 0; s-- {
		detail := layers[s].Detail
		for i := 0; i < total; i++ {
			if support[i] == 0 {
				bg[i] += detail[i]
			}
		}
	}

	return MRSResult{Background: to2D(bg, w, h), NoiseSigma: noiseSigma, ImageSNR: estimateImageSNRFromLayers(layers, noiseSigma, w, h, scales)}
}

func estimateImageSNRFromLayers(layers []ATrousLayerFlat, noiseSigma float64, w, h, scales int) float64 {
	kSigma := 3.0

	if noiseSigma <= 0 {
		return 0
	}

	total := w * h
	var totalSignalPower, totalNoisePower float64

	for s := 0; s < scales; s++ {
		scaleThreshold := kSigma * noiseSigma * ScaleNoiseFactors[s]
		sigmaJ := noiseSigma * ScaleNoiseFactors[s]
		detail := layers[s].Detail

		var sigPower float64
		for i := 0; i < total; i++ {
			coeff := detail[i]
			if math.Abs(coeff) > scaleThreshold {
				sigPower += coeff * coeff
			}
		}

		totalSignalPower += sigPower
		totalNoisePower += float64(total) * sigmaJ * sigmaJ
	}

	if totalNoisePower <= 0 {
		return 0
	}
	return math.Sqrt(totalSignalPower / totalNoisePower)
}

// bin2x2Mean downsamples a flat image by 2x2 mean binning. Odd trailing
// rows/columns are dropped. White-noise std drops by ~2x (binNoiseFactor).
func bin2x2Mean(flat []float64, w, h int) ([]float64, int, int) {
	bw, bh := w/2, h/2
	binned := make([]float64, bw*bh)
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			i := (2*y)*w + 2*x
			binned[y*bw+x] = (flat[i] + flat[i+1] + flat[i+w] + flat[i+w+1]) * 0.25
		}
	}
	return binned, bw, bh
}

// binNoiseFactor rescales noise sigma estimated on a 2x2 mean-binned image
// back to full-resolution pixel units (white-noise assumption).
const binNoiseFactor = 2.0

// binSNRFactor rescales SNR estimated on the binned grid to
// full-resolution equivalent units. Calibrated by A/B measurement
// against the full-resolution estimator on real frames
// (binned SNR runs ~1.95x full SNR: 1.967/1.933/1.960).
var binSNRFactor = 0.5

// upsampleBilinear interpolates a small flat map back to full resolution.
func upsampleBilinear(small []float64, sw, sh, w, h int) []float64 {
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		sy := float64(y) * float64(sh) / float64(h)
		y0 := int(sy)
		if y0 >= sh-1 {
			y0 = sh - 1
		}
		y1 := y0 + 1
		if y1 >= sh {
			y1 = sh - 1
		}
		fy := sy - float64(y0)
		if y0 == sh-1 {
			fy = 0
		}
		for x := 0; x < w; x++ {
			sx := float64(x) * float64(sw) / float64(w)
			x0 := int(sx)
			if x0 >= sw-1 {
				x0 = sw - 1
			}
			x1 := x0 + 1
			if x1 >= sw {
				x1 = sw - 1
			}
			fx := sx - float64(x0)
			if x0 == sw-1 {
				fx = 0
			}
			a := small[y0*sw+x0]
			b := small[y0*sw+x1]
			c := small[y1*sw+x0]
			d := small[y1*sw+x1]
			out[y*w+x] = a + (b-a)*fx + (c-a)*fy + (a-b-c+d)*fx*fy
		}
	}
	return out
}

// EstimateBackgroundAndNoiseBinned runs the MRS estimation on a 2x2
// mean-binned image (~4x fewer pixels) with one fewer scale — binning
// approximates dropping the finest scale — then upsamples the background
// to full resolution. NoiseSigma is rescaled to full-resolution units.
// ~4-8x faster than EstimateBackgroundAndNoise with small metric shifts.
func EstimateBackgroundAndNoiseBinned(image [][]float64, scales int) MRSResult {
	kSigma := 3.0

	flat, w, h := toFlat(image)
	binned, bw, bh := bin2x2Mean(flat, w, h)

	bscales := scales - 1
	if bscales < 1 {
		bscales = 1
	}
	layers := ATrousTransformFlat(binned, bw, bh, bscales)
	noiseSigma := EstimateNoiseSigmaFlat(layers[0].Detail, bw, bh, ScaleNoiseFactors[0]) * binNoiseFactor

	total := bw * bh
	support := make([]int, total)
	for s := 0; s < bscales-1; s++ {
		scaleThreshold := kSigma * noiseSigma / binNoiseFactor * ScaleNoiseFactors[s]
		detail := layers[s].Detail
		for i := 0; i < total; i++ {
			if support[i] == 0 && math.Abs(detail[i]) > scaleThreshold {
				support[i] = 1
			}
		}
	}

	bg := make([]float64, total)
	copy(bg, layers[bscales-1].Smooth)
	for s := bscales - 2; s >= 0; s-- {
		detail := layers[s].Detail
		for i := 0; i < total; i++ {
			if support[i] == 0 {
				bg[i] += detail[i]
			}
		}
	}

	// SNR is estimated self-consistently on the binned grid (binned sigma),
	// then rescaled to full-resolution equivalent units (see binSNRFactor).
	binnedSigma := noiseSigma / binNoiseFactor
	return MRSResult{Background: to2D(upsampleBilinear(bg, bw, bh, w, h), w, h), NoiseSigma: noiseSigma, ImageSNR: estimateImageSNRFromLayers(layers, binnedSigma, bw, bh, bscales) * binSNRFactor}
}

func EstimateBackgroundMap(image [][]float64, scales int) [][]float64 {
	return EstimateBackgroundAndNoise(image, scales).Background
}

func EstimateNoiseSigmaFromImage(image [][]float64, scales int) float64 {
	return EstimateBackgroundAndNoise(image, scales).NoiseSigma
}

func ComputeMRS(image [][]float64, scales int, kSigma float64) ([][][]int, float64) {
	height := len(image)
	width := len(image[0])

	flat, w, h := toFlat(image)
	layers := ATrousTransformFlat(flat, w, h, scales)
	noiseSigma := EstimateNoiseSigmaFlat(layers[0].Detail, w, h, ScaleNoiseFactors[0])

	supportMasks := make([][][]int, scales)
	for s := 0; s < scales; s++ {
		supportMasks[s] = make([][]int, height)
		scaleThreshold := kSigma * noiseSigma * ScaleNoiseFactors[s]
		detail := layers[s].Detail
		for i := 0; i < height; i++ {
			supportMasks[s][i] = make([]int, width)
			row := supportMasks[s][i]
			base := i * width
			for j := 0; j < width; j++ {
				if math.Abs(detail[base+j]) > scaleThreshold {
					row[j] = 1
				}
			}
		}
	}
	return supportMasks, noiseSigma
}

func ATrousTransformFlat(input []float64, width, height, scales int) []ATrousLayerFlat {
	layers := make([]ATrousLayerFlat, scales)
	currentSmooth := make([]float64, len(input))
	copy(currentSmooth, input)
	numCPU := ConvWorkers
	if numCPU < 1 {
		numCPU = 1
	}

	for s := 0; s < scales; s++ {
		step := 1 << uint(s)
		nextSmooth := Convolve2DFlat(currentSmooth, width, height, step, numCPU)

		detail := make([]float64, len(input))
		for i := range detail {
			detail[i] = currentSmooth[i] - nextSmooth[i]
		}
		layers[s] = ATrousLayerFlat{Smooth: nextSmooth, Detail: detail}
		currentSmooth = nextSmooth
	}
	return layers
}

func Convolve2DFlat(input []float64, width, height, step, numCPU int) []float64 {
	if numCPU < 1 {
		numCPU = 1
	}
	total := width * height
	temp := make([]float64, total)
	output := make([]float64, total)

	var wg sync.WaitGroup
	rowsPerWorker := (height + numCPU - 1) / numCPU

	// Horizontal pass
	wg.Add(numCPU)
	for w := 0; w < numCPU; w++ {
		go func(worker int) {
			defer wg.Done()
			start := worker * rowsPerWorker
			end := start + rowsPerWorker
			if end > height {
				end = height
			}
			for i := start; i < end; i++ {
				base := i * width
				for j := 0; j < width; j++ {
					var sum float64
					for f := -2; f <= 2; f++ {
						idx := j + f*step
						if idx < 0 {
							idx = -idx
						}
						if idx >= width {
							idx = 2*width - 2 - idx
						}
						sum += input[base+idx] * B3SplineFilter[f+2]
					}
					temp[base+j] = sum
				}
			}
		}(w)
	}
	wg.Wait()

	// Vertical pass
	wg.Add(numCPU)
	for w := 0; w < numCPU; w++ {
		go func(worker int) {
			defer wg.Done()
			start := worker * rowsPerWorker
			end := start + rowsPerWorker
			if end > height {
				end = height
			}
			for i := start; i < end; i++ {
				for j := 0; j < width; j++ {
					var sum float64
					for f := -2; f <= 2; f++ {
						idx := i + f*step
						if idx < 0 {
							idx = -idx
						}
						if idx >= height {
							idx = 2*height - 2 - idx
						}
						sum += temp[idx*width+j] * B3SplineFilter[f+2]
					}
					output[i*width+j] = sum
				}
			}
		}(w)
	}
	wg.Wait()

	return output
}

func Convolve2D(input [][]float64, step int) [][]float64 {
	flat, w, h := toFlat(input)
	out := Convolve2DFlat(flat, w, h, step, ConvWorkers)
	return to2D(out, w, h)
}

func ATrousTransform(image [][]float64, scales int) []ATrousLayer {
	height := len(image)
	width := len(image[0])
	flat, w, h := toFlat(image)
	flatLayers := ATrousTransformFlat(flat, w, h, scales)

	layers := make([]ATrousLayer, len(flatLayers))
	for i, fl := range flatLayers {
		layers[i] = ATrousLayer{
			Smooth: to2D(fl.Smooth, width, height),
			Detail: to2D(fl.Detail, width, height),
		}
	}
	return layers
}

type ATrousLayer struct {
	Smooth [][]float64
	Detail [][]float64
}

func EstimateNoiseSigma(detailLayer [][]float64, scaleFactor float64) float64 {
	flat, w, h := toFlat(detailLayer)
	return EstimateNoiseSigmaFlat(flat, w, h, scaleFactor)
}

// maxMadSamples caps the MAD sample count so noise estimation stays O(200k)
// with quickselect medians instead of full sorts. Detail coefficients are
// spatially smooth, so stride sampling is statistically equivalent.
const maxMadSamples = 200000

// quickselect reorders vals so vals[k] is the k-th smallest element.
func quickselect(vals []float64, k int) {
	lo, hi := 0, len(vals)-1
	for lo < hi {
		pivot := vals[(lo+hi)/2]
		i, j := lo, hi
		for i <= j {
			for vals[i] < pivot {
				i++
			}
			for vals[j] > pivot {
				j--
			}
			if i <= j {
				vals[i], vals[j] = vals[j], vals[i]
				i++
				j--
			}
		}
		if j < k {
			lo = i
		}
		if k < i {
			hi = j
		}
	}
}

func medianSelect(vals []float64) float64 {
	n := len(vals)
	if n%2 == 1 {
		quickselect(vals, n/2)
		return vals[n/2]
	}
	quickselect(vals, n/2-1)
	a := vals[n/2-1]
	quickselect(vals, n/2)
	return (a + vals[n/2]) / 2.0
}

func EstimateNoiseSigmaFlat(detailLayer []float64, width, height int, scaleFactor float64) float64 {
	total := width * height

	step := 1
	if total > maxMadSamples {
		step = int(math.Ceil(math.Sqrt(float64(total) / maxMadSamples)))
		if step < 1 {
			step = 1
		}
	}

	var values []float64
	capacity := (height/step + 1) * (width/step + 1)
	if capacity > maxMadSamples+width {
		capacity = maxMadSamples + width
	}
	values = make([]float64, 0, capacity)

	for i := 0; i < total; i += step * width {
		for j := 0; j < width; j += step {
			values = append(values, detailLayer[i+j])
		}
	}

	n := len(values)
	if n == 0 {
		return 0
	}

	med := medianSelect(values)

	// Reuse the buffer for absolute deviations to avoid a second allocation.
	for i, val := range values {
		d := val - med
		if d < 0 {
			d = -d
		}
		values[i] = d
	}
	mad := medianSelect(values)
	if mad == 0 {
		return 0
	}
	return mad / (0.6745 * scaleFactor)
}
