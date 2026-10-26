package starsmetrics

import "math"

// fwhmPerSigma converts a Gaussian sigma to a full width at half maximum.
const fwhmPerSigma = 2.3548200450309493

// StarFit is the shape of one star recovered by fitting an elliptical Gaussian
// to its pixels. It replaces the moment-based estimators for the reported
// metrics: the fit has the PSF's own profile built in, so an undersampled star
// is not mistaken for a broad one.
type StarFit struct {
	X, Y          float64 // centroid, sub-pixel
	Background    float64
	Amplitude     float64
	SigmaX        float64 // along the major axis
	SigmaY        float64 // along the minor axis
	FWHM          float64 // equivalent circular FWHM, the geometric mean
	FWHMMajor     float64
	Eccentricity  float64
	PositionAngle float64 // degrees, 0-180, major axis
}

// Gaussian parameters. The exponent is written as
//
//	a*dx^2 + b*dx*dy + c*dy^2
//
// so that the covariance matrix is the inverse of [[a, b/2], [b/2, c]]. A
// circular Gaussian has b == 0 and a == c.
type gaussFit struct{ bg, amp, x0, y0, a, b, c float64 }

func (p gaussFit) at(x, y float64) float64 {
	dx := x - p.x0
	dy := y - p.y0
	q := p.a*dx*dx + p.b*dx*dy + p.c*dy*dy
	if q > 700 { // exp underflows long before this
		return p.bg
	}
	return p.bg + p.amp*math.Exp(-0.5*q)
}

// valid rejects parameters that cannot describe a star: a non-positive
// amplitude, or a covariance that is not positive definite. Those are the
// states a damped least-squares step wanders into when the data are too noisy
// for seven free parameters.
func (p gaussFit) valid() bool {
	if p.amp <= 0 || math.IsNaN(p.amp) || math.IsInf(p.amp, 0) {
		return false
	}
	for _, v := range []float64{p.bg, p.x0, p.y0, p.a, p.b, p.c} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	if p.a <= 0 || p.c <= 0 {
		return false
	}
	return p.a*p.c-p.b*p.b/4 > 0
}

// FitEllipticalGaussian fits background + elliptical Gaussian to the pixels in a
// square box of the given half-width around (cx, cy).
//
// This is the replacement for the moment estimators. Moments sum (pixel - bg)
// over the box with the background taken from a smoothed map and negatives
// clipped to zero, which biases both width and shape whenever the PSF spans only
// a couple of pixels: a 2 px PSF in a nebula field measured FWHM 1.96 and
// eccentricity 0.64 where a PSF fit on the same frames gives 1.24 and 0.44.
// Fitting the profile instead makes the background a free parameter and removes
// the clipping bias entirely.
//
// The background is a constant over the box rather than a plane. A plane is the
// obvious next step for nebula fields, but it adds two parameters to an already
// ill-conditioned fit; the constant is enough to remove the pedestal that the
// clipping introduced, and the box is kept small so a real gradient cannot move
// much across it.
func FitEllipticalGaussian(pixels, bgMap [][]float64, cx, cy, halfWidth int) (StarFit, bool) {
	height := len(pixels)
	if height == 0 {
		return StarFit{}, false
	}
	width := len(pixels[0])

	useBgMap := len(bgMap) == height && len(bgMap) > 0 && len(bgMap[0]) == width

	var (
		xs, ys, zs []float64
		peak       = math.Inf(-1)
		peakX      int
		peakY      int
	)
	for y := cy - halfWidth; y <= cy+halfWidth; y++ {
		if y < 0 || y >= height {
			continue
		}
		for x := cx - halfWidth; x <= cx+halfWidth; x++ {
			if x < 0 || x >= width {
				continue
			}
			z := pixels[y][x]
			xs = append(xs, float64(x))
			ys = append(ys, float64(y))
			zs = append(zs, z)
			if z > peak {
				peak, peakX, peakY = z, x, y
			}
		}
	}
	if len(xs) < 9 {
		return StarFit{}, false
	}

	bg0 := 0.0
	if useBgMap {
		bg0 = bgMap[cy][cx]
	}

	p := initialGauss(xs, ys, zs, bg0, peak, peakX, peakY)
	if !p.valid() {
		return StarFit{}, false
	}

	if !levenbergMarquardt(xs, ys, zs, &p) {
		return StarFit{}, false
	}
	if !p.valid() {
		return StarFit{}, false
	}

	// The Gaussian must explain the data better than a flat background alone.
	// Without this a constant image fits a very broad, very faint source: the
	// parameters are finite and the covariance is positive definite, so
	// valid() alone cannot tell it from a star.
	var chiFlat float64
	for i := range zs {
		d := p.bg - zs[i]
		chiFlat += d * d
	}
	if chiFlat <= 0 || chiSquared(xs, ys, zs, p) > 0.9*chiFlat {
		return StarFit{}, false
	}

	// Eigenvalues of the covariance inverse give the two axis sigmas.
	trace := p.a + p.c
	det := p.a*p.c - p.b*p.b/4
	disc := trace*trace - 4*det
	if disc < 0 {
		return StarFit{}, false
	}
	root := math.Sqrt(disc)
	lambdaMax := (trace + root) / 2
	lambdaMin := (trace - root) / 2
	if lambdaMin <= 0 || lambdaMax <= 0 {
		return StarFit{}, false
	}

	sigmaMajor := 1 / math.Sqrt(lambdaMin)
	sigmaMinor := 1 / math.Sqrt(lambdaMax)
	// A sigma wider than the box is not a star inside that box; it is the fit
	// absorbing a background slope or a neighbour.
	if sigmaMajor < 0.2 || sigmaMinor < 0.2 ||
		sigmaMajor > float64(halfWidth) || sigmaMinor > float64(halfWidth) {
		return StarFit{}, false
	}

	fit := StarFit{
		X:             p.x0,
		Y:             p.y0,
		Background:    p.bg,
		Amplitude:     p.amp,
		SigmaX:        sigmaMajor,
		SigmaY:        sigmaMinor,
		FWHM:          fwhmPerSigma * math.Sqrt(sigmaMajor*sigmaMinor),
		FWHMMajor:     fwhmPerSigma * sigmaMajor,
		Eccentricity:  math.Sqrt(1 - (sigmaMinor*sigmaMinor)/(sigmaMajor*sigmaMajor)),
		PositionAngle: 0.5 * math.Atan2(p.b, p.a-p.c) * 180 / math.Pi,
	}
	if math.IsNaN(fit.Eccentricity) || math.IsInf(fit.Eccentricity, 0) {
		return StarFit{}, false
	}
	return fit, true
}

// initialGauss seeds the fit from the intensity-weighted moments of the positive
// residuals. The moments are the bad estimator as a *result*, but as a starting
// point they are fine: they are biased, not random, and the fit refines from
// there. Seeding from the peak pixel alone converges more slowly and can lock
// onto a neighbouring pixel's shoulder.
func initialGauss(xs, ys, zs []float64, bg0, peak float64, peakX, peakY int) gaussFit {
	var m00, m10, m01, m20, m02, m11 float64
	for i := range xs {
		w := zs[i] - bg0
		if w <= 0 {
			continue
		}
		dx := xs[i]
		dy := ys[i]
		m00 += w
		m10 += dx * w
		m01 += dy * w
		m20 += dx * dx * w
		m02 += dy * dy * w
		m11 += dx * dy * w
	}

	p := gaussFit{
		bg:  bg0,
		amp: peak - bg0,
		x0:  float64(peakX),
		y0:  float64(peakY),
		a:   1.0 / (2 * 1.0 * 1.0),
		c:   1.0 / (2 * 1.0 * 1.0),
	}
	if m00 <= 0 {
		return p
	}

	x0 := m10 / m00
	y0 := m01 / m00
	mu20 := m20/m00 - x0*x0
	mu02 := m02/m00 - y0*y0
	mu11 := m11/m00 - x0*y0
	det := mu20*mu02 - mu11*mu11
	if det <= 0 || mu20 <= 0 || mu02 <= 0 {
		return p
	}

	// Invert the covariance into the quadratic form. The moments include the
	// intra-pixel variance (about 1/12 per axis), which is left in on purpose:
	// it makes the seed slightly broad, and a broad seed converges.
	p.x0 = x0
	p.y0 = y0
	p.a = mu02 / det
	p.c = mu20 / det
	p.b = -2 * mu11 / det
	if !p.valid() {
		return gaussFit{bg: bg0, amp: peak - bg0, x0: float64(peakX), y0: float64(peakY),
			a: 0.5, c: 0.5}
	}
	return p
}

// levenbergMarquardt minimises the sum of squared residuals in place.
//
// Damped least squares rather than plain Gauss-Newton because seven parameters
// over an undersampled star is weakly constrained: the undamped step overshoots
// into a negative sigma, and the damping is what keeps it inside the region
// where the Gaussian means something.
func levenbergMarquardt(xs, ys, zs []float64, p *gaussFit) bool {
	const (
		maxIter = 200
		eps     = 1e-12
	)
	lambda := 1e-3
	chi := chiSquared(xs, ys, zs, *p)
	if math.IsInf(chi, 0) || math.IsNaN(chi) {
		return false
	}

	params := []*float64{&p.bg, &p.amp, &p.x0, &p.y0, &p.a, &p.b, &p.c}

	for iter := 0; iter < maxIter; iter++ {
		var jtj [7][7]float64
		var jtr [7]float64

		for i := range xs {
			res := p.at(xs[i], ys[i]) - zs[i]
			var j [7]float64
			for k := 0; k < 7; k++ {
				orig := *params[k]
				step := 1e-6 * math.Max(1, math.Abs(orig))
				*params[k] = orig + step
				plus := p.at(xs[i], ys[i])
				*params[k] = orig
				j[k] = (plus - p.at(xs[i], ys[i])) / step
			}
			for a := 0; a < 7; a++ {
				jtr[a] += j[a] * res
				for b := 0; b < 7; b++ {
					jtj[a][b] += j[a] * j[b]
				}
			}
		}

		// Damp the diagonal, scaled so parameters of different magnitude are
		// treated alike.
		var damped [7][7]float64
		for a := 0; a < 7; a++ {
			for b := 0; b < 7; b++ {
				damped[a][b] = jtj[a][b]
			}
			damped[a][a] += lambda * math.Max(jtj[a][a], 1e-12)
		}

		var rhs [7]float64
		for a := 0; a < 7; a++ {
			rhs[a] = -jtr[a]
		}
		delta, ok := solve7(damped, rhs)
		if !ok {
			return false
		}

		trial := *p
		for k := 0; k < 7; k++ {
			*params[k] = *params[k] + delta[k]
		}
		trial = *p
		trialChi := chiSquared(xs, ys, zs, trial)

		if trial.valid() && trialChi < chi {
			improvement := (chi - trialChi) / math.Max(chi, eps)
			*p = trial
			chi = trialChi
			lambda = math.Max(lambda/3, 1e-12)
			if improvement < 1e-10 {
				return true
			}
			continue
		}

		// Reject the step and try again from the old parameters.
		for k := 0; k < 7; k++ {
			*params[k] = *params[k] - delta[k]
		}
		lambda *= 5
		if lambda > 1e10 {
			return false
		}
		if iter > 0 && trialChi >= chi && lambda > 1e6 {
			return true // converged as far as the damping allows
		}
	}
	return true
}

func chiSquared(xs, ys, zs []float64, p gaussFit) float64 {
	var chi float64
	for i := range xs {
		r := p.at(xs[i], ys[i]) - zs[i]
		chi += r * r
	}
	return chi
}

// solve7 solves a 7x7 system by Gaussian elimination with partial pivoting.
func solve7(a [7][7]float64, b [7]float64) ([7]float64, bool) {
	var out [7]float64
	for col := 0; col < 7; col++ {
		pivot := col
		for r := col + 1; r < 7; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(a[pivot][col]) < 1e-18 {
			return out, false
		}
		a[col], a[pivot] = a[pivot], a[col]
		b[col], b[pivot] = b[pivot], b[col]

		inv := 1 / a[col][col]
		for c := col; c < 7; c++ {
			a[col][c] *= inv
		}
		b[col] *= inv

		for r := 0; r < 7; r++ {
			if r == col || a[r][col] == 0 {
				continue
			}
			f := a[r][col]
			for c := col; c < 7; c++ {
				a[r][c] -= f * a[col][c]
			}
			b[r] -= f * b[col]
		}
	}
	copy(out[:], b[:])
	return out, true
}
