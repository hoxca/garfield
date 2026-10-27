package starsmetrics

import "math"

// fwhmPerSigma converts a Gaussian sigma to a full width at half maximum.
const fwhmPerSigma = 2.3548200450309493

// StarFit is the shape of one star recovered by fitting an elliptical Moffat
// profile to its pixels. It replaces the moment-based estimators for the
// reported metrics: the fit has the PSF's own profile built in, so an
// undersampled star is not mistaken for a broad one.
type StarFit struct {
	X, Y          float64 // centroid, sub-pixel
	Background    float64
	Amplitude     float64
	Beta          float64 // Moffat exponent; large means Gaussian wings
	SigmaX        float64 // equivalent Gaussian sigma, major axis
	SigmaY        float64 // equivalent Gaussian sigma, minor axis
	FWHM          float64 // equivalent circular FWHM, the geometric mean
	FWHMMajor     float64
	Eccentricity  float64
	PositionAngle float64 // degrees, 0-180, major axis
}

// Moffat parameters. The exponent is written as
//
//	a*dx^2 + b*dx*dy + c*dy^2
//
// so that the covariance matrix is the inverse of [[a, b/2], [b/2, c]]. A
// circular profile has b == 0 and a == c.
//
// The profile is
//
//	bg + amp * (1 + q/(2*beta))^-beta
//
// which tends to bg + amp*exp(-q/2) as beta grows. A Gaussian is therefore the
// beta -> infinity limit of the same parameterisation, and the two models share
// every other parameter: beta is the only extra unknown.
type profileFit struct{ bg, amp, x0, y0, a, b, c, beta float64 }

// gaussianBeta is the point past which the Moffat is indistinguishable from a
// Gaussian to well under a per cent. Above it the exponential form is used
// directly: it is faster, and it makes the Gaussian limit exact rather than
// merely close, so a well-sampled PSF is not fitted with a residual error.
const gaussianBeta = 100.0

func (p profileFit) at(x, y float64) float64 {
	f, _ := p.derivs(x, y)
	return f
}

// derivs returns the model value and its partial derivatives with respect to
// [bg, amp, x0, y0, a, b, c, beta] at (x, y).
//
// The derivatives are closed forms, which is what makes the fit cheap: a
// numerical Jacobian evaluates the model 17 times per pixel per iteration (a
// baseline plus a plus-and-minus pair per parameter). This evaluates it once.
func (p profileFit) derivs(x, y float64) (f float64, d [8]float64) {
	dx := x - p.x0
	dy := y - p.y0
	q := p.a*dx*dx + p.b*dx*dy + p.c*dy*dy

	var shape, common float64
	if p.beta >= gaussianBeta {
		shape = math.Exp(-0.5 * q)
		// d/dq of the profile. For the Gaussian limit the beta derivative is
		// zero by construction: beta is pinned, not optimised.
		common = -0.5 * shape
	} else {
		r := 1 + q/(2*p.beta)
		if r <= 0 {
			r = 1e-12
		}
		shape = math.Exp(-p.beta * math.Log(r))
		common = -shape / (2 * r)
		// d/dbeta, the parameter the Gaussian does not have.
		d[7] = p.amp * shape * (q/(2*p.beta*r) - math.Log(r))
	}

	f = p.bg + p.amp*shape

	d[0] = 1
	d[1] = shape
	d[2] = p.amp * common * (-2*p.a*dx - p.b*dy)
	d[3] = p.amp * common * (-p.b*dx - 2*p.c*dy)
	d[4] = p.amp * common * dx * dx
	d[5] = p.amp * common * dx * dy
	d[6] = p.amp * common * dy * dy
	return f, d
}

// valid rejects parameters that cannot describe a star: a non-positive
// amplitude, a covariance that is not positive definite, or a Moffat exponent
// outside the range where the profile means anything. Those are the states a
// damped least-squares step wanders into when the data are too noisy for eight
// free parameters.
func (p profileFit) valid() bool {
	if p.amp <= 0 || math.IsNaN(p.amp) || math.IsInf(p.amp, 0) {
		return false
	}
	for _, v := range []float64{p.bg, p.x0, p.y0, p.a, p.b, p.c, p.beta} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	if p.beta < 1 || p.beta > gaussianBeta {
		return false
	}
	if p.a <= 0 || p.c <= 0 {
		return false
	}
	return p.a*p.c-p.b*p.b/4 > 0
}

// FitStarProfile fits background + elliptical Moffat profile to the pixels in a
// square box of the given half-width around (cx, cy), and reports the FWHM and
// eccentricity of the fitted half-maximum contour.
//
// This is the replacement for the moment estimators. Moments sum (pixel - bg)
// over the box with the background taken from a smoothed map and negatives
// clipped to zero, which biases both width and shape whenever the PSF spans only
// a couple of pixels: a 2 px PSF in a nebula field measured FWHM 1.96 and
// eccentricity 0.64 where a PSF fit on the same frames gives 1.24 and 0.44.
// Fitting the profile instead makes the background a free parameter and removes
// the clipping bias entirely.
//
// The profile is chosen by moffat. Gaussian mode pins beta at the Gaussian limit
// and fits only the seven shape parameters; it is roughly five times cheaper.
// Moffat mode fits beta as an eighth parameter and costs more, because a real PSF
// has broader wings than a Gaussian can express: on the NGC281 frames the
// Gaussian fit read FWHM ~1.6 px against PixInsight's Moffat-based 1.24, while
// its eccentricity — a ratio of axes, and so insensitive to the wing shape —
// already agreed. Beta is what lets the width agree too.
//
// The background is a constant over the box rather than a plane. A plane is the
// obvious next step for nebula fields, but it adds two parameters to an already
// ill-conditioned fit; the constant is enough to remove the pedestal that the
// clipping introduced, and the box is kept small so a real gradient cannot move
// much across it.
func FitStarProfile(pixels, bgMap [][]float64, cx, cy, halfWidth int, moffat bool) (StarFit, bool) {
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

	p := initialProfile(xs, ys, zs, bg0, peak, peakX, peakY)
	if !moffat {
		// Pin beta at the Gaussian limit. The normal-equation row for beta is
		// zeroed when it is pinned, so this is the same seven-parameter fit the
		// Gaussian version performed, not an approximation of it.
		p.beta = gaussianBeta
	}
	if !p.valid() {
		return StarFit{}, false
	}

	okLM := levenbergMarquardt(xs, ys, zs, &p)
	if !okLM {
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

	// Eigenvalues of the covariance inverse give the two axis directions.
	trace := p.a + p.c
	det := p.a*p.c - p.b*p.b/4
	// disc is (a-c)^2 + b^2, so it cannot be negative in exact arithmetic. For a
	// perfectly round star a == c and the subtraction lands on -1e-16 or so;
	// rejecting there discarded every circular source.
	disc := trace*trace - 4*det
	if disc < 0 {
		disc = 0
	}
	root := math.Sqrt(disc)
	lambdaMax := (trace + root) / 2
	lambdaMin := (trace - root) / 2
	if lambdaMin <= 0 || lambdaMax <= 0 {
		return StarFit{}, false
	}

	// Half maximum of the profile in the quadratic form's units. Along an
	// eigen-direction q = lambda*u^2, so the half-width is sqrt(qHalf/lambda)
	// and the full width is twice that. For a Gaussian qHalf is 2ln2, which
	// reduces to the familiar 2.3548*sigma.
	qHalf := 2 * math.Ln2
	if p.beta < gaussianBeta {
		qHalf = 2 * p.beta * (math.Pow(2, 1/p.beta) - 1)
	}

	fwhmMajor := 2 * math.Sqrt(qHalf/lambdaMin)
	fwhmMinor := 2 * math.Sqrt(qHalf/lambdaMax)
	if fwhmMajor < 0.2 || fwhmMinor < 0.2 ||
		fwhmMajor > float64(2*halfWidth) || fwhmMinor > float64(2*halfWidth) {
		return StarFit{}, false
	}

	fit := StarFit{
		X:             p.x0,
		Y:             p.y0,
		Background:    p.bg,
		Amplitude:     p.amp,
		Beta:          p.beta,
		SigmaX:        fwhmMajor / fwhmPerSigma,
		SigmaY:        fwhmMinor / fwhmPerSigma,
		FWHM:          math.Sqrt(fwhmMajor * fwhmMinor),
		FWHMMajor:     fwhmMajor,
		Eccentricity:  math.Sqrt(1 - (fwhmMinor*fwhmMinor)/(fwhmMajor*fwhmMajor)),
		PositionAngle: 0.5 * math.Atan2(p.b, p.a-p.c) * 180 / math.Pi,
	}
	if math.IsNaN(fit.Eccentricity) || math.IsInf(fit.Eccentricity, 0) {
		return StarFit{}, false
	}
	return fit, true
}

// initialProfile seeds the fit from the intensity-weighted moments of the
// positive residuals. The moments are the bad estimator as a *result*, but as a
// starting point they are fine: they are biased, not random, and the fit refines
// from there. Seeding from the peak pixel alone converges more slowly and can
// lock onto a neighbouring pixel's shoulder.
func initialProfile(xs, ys, zs []float64, bg0, peak float64, peakX, peakY int) profileFit {
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

	p := profileFit{
		bg:   bg0,
		amp:  peak - bg0,
		x0:   float64(peakX),
		y0:   float64(peakY),
		a:    0.5,
		c:    0.5,
		beta: 20, // start near the Gaussian limit; the data pull it down
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
		return profileFit{bg: bg0, amp: peak - bg0, x0: float64(peakX), y0: float64(peakY),
			a: 0.5, c: 0.5, beta: 20}
	}
	return p
}

// levenbergMarquardt minimises the sum of squared residuals in place.
//
// Damped least squares rather than plain Gauss-Newton because eight parameters
// over an undersampled star is weakly constrained: the undamped step overshoots
// into a negative sigma, and the damping is what keeps it inside the region
// where the profile means something.
func levenbergMarquardt(xs, ys, zs []float64, p *profileFit) bool {
	const (
		maxIter = 200
		eps     = 1e-12
		dim     = 8
	)
	lambda := 1e-3
	chi := chiSquared(xs, ys, zs, *p)
	if math.IsInf(chi, 0) || math.IsNaN(chi) {
		return false
	}

	for iter := 0; iter < maxIter; iter++ {
		var jtj [dim][dim]float64
		var jtr [dim]float64

		for i := range xs {
			model, j := p.derivs(xs[i], ys[i])
			res := model - zs[i]
			for a := 0; a < dim; a++ {
				jtr[a] += j[a] * res
				for b := 0; b < dim; b++ {
					jtj[a][b] += j[a] * j[b]
				}
			}
		}

		// A pinned beta contributes a zero column: at the Gaussian limit the
		// derivative with respect to beta is identically zero, so the normal
		// equations are singular and the solve would fail on a perfectly good
		// star. Pin the parameter in the system as well as in the model.
		if p.beta >= gaussianBeta {
			for k := 0; k < dim; k++ {
				jtj[7][k] = 0
				jtj[k][7] = 0
			}
			jtj[7][7] = 1
			jtr[7] = 0
		}

		// Damp the diagonal, scaled so parameters of different magnitude are
		// treated alike.
		var damped [dim][dim]float64
		for a := 0; a < dim; a++ {
			for b := 0; b < dim; b++ {
				damped[a][b] = jtj[a][b]
			}
			damped[a][a] += lambda * math.Max(jtj[a][a], 1e-12)
		}

		var rhs [dim]float64
		for a := 0; a < dim; a++ {
			rhs[a] = -jtr[a]
		}
		delta, ok := solve(damped, rhs)
		if !ok {
			return false
		}
		delta = clampDelta(delta, *p)

		// Build the trial from a copy. Mutating p and undoing the delta on
		// rejection would not restore the value, because the clamps below are
		// not linear in the delta.
		trial := *p
		tp := []*float64{&trial.bg, &trial.amp, &trial.x0, &trial.y0,
			&trial.a, &trial.b, &trial.c, &trial.beta}
		for k := 0; k < dim; k++ {
			*tp[k] += delta[k]
		}
		// beta saturates at the Gaussian limit rather than crossing it. Without
		// this the step can land at 101, valid() rejects it, and the damping
		// escalates until the whole fit gives up on a perfectly good star.
		if trial.beta > gaussianBeta {
			trial.beta = gaussianBeta
		}
		if trial.beta < 1 {
			trial.beta = 1
		}
		// Keep the quadratic form strictly positive definite. For a perfectly
		// round star a and c are exchangeable, so b has no gradient holding it
		// down and can drift until the ellipse degenerates -- at which point the
		// axis ratio is meaningless and the fit is discarded. A little inside
		// the cone is enough.
		if spread := 1.8 * math.Sqrt(trial.a*trial.c); trial.b > spread {
			trial.b = spread
		} else if trial.b < -spread {
			trial.b = -spread
		}

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

		lambda *= 5
		if lambda > 1e8 {
			// Damping exhausted with no step that improves chi. That is the
			// minimum as far as this box can see, not a failure: the caller
			// checks the parameters for plausibility separately. Reporting false
			// here discarded good fits, including every noise-free one, where
			// chi reaches its floor and no strict improvement is possible.
			return true
		}
	}
	return true
}

// clampDelta bounds one least-squares step.
//
// The linear parameters (background, amplitude) are well conditioned and need no
// help. The shape parameters are not: the Moffat exponent's column in the normal
// matrix is orders of magnitude smaller than the others, so an undamped step
// moves beta by hundreds and leaves the profile's valid range immediately. The
// clamp is a trust region on the parameters whose scale is not comparable,
// expressed in units that mean something: pixels for the centroid, a fraction of
// the current width for the quadratic terms, and a modest range for beta.
func clampDelta(d [8]float64, p profileFit) [8]float64 {
	ab := math.Sqrt(math.Max(p.a*p.c, 1e-6))
	limit := [8]float64{
		math.Inf(1), // bg: linear, well scaled
		math.Inf(1), // amp: linear, well scaled
		1.0,         // x0, pixels
		1.0,         // y0, pixels
		0.5 * math.Max(math.Abs(p.a), 1e-3),
		0.5 * math.Max(math.Abs(p.b), ab),
		0.5 * math.Max(math.Abs(p.c), 1e-3),
		2.0, // beta
	}
	for i := range d {
		if d[i] > limit[i] {
			d[i] = limit[i]
		} else if d[i] < -limit[i] {
			d[i] = -limit[i]
		}
	}
	return d
}

func chiSquared(xs, ys, zs []float64, p profileFit) float64 {
	var chi float64
	for i := range xs {
		r := p.at(xs[i], ys[i]) - zs[i]
		chi += r * r
	}
	return chi
}

// solve solves the normal equations by Gaussian elimination with partial
// pivoting. The dimension is fixed at eight, which is the fit's parameter count.
func solve(a [8][8]float64, b [8]float64) ([8]float64, bool) {
	const dim = 8
	var out [dim]float64
	for col := 0; col < dim; col++ {
		pivot := col
		for r := col + 1; r < dim; r++ {
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
		for c := col; c < dim; c++ {
			a[col][c] *= inv
		}
		b[col] *= inv

		for r := 0; r < dim; r++ {
			if r == col || a[r][col] == 0 {
				continue
			}
			f := a[r][col]
			for c := col; c < dim; c++ {
				a[r][c] -= f * a[col][c]
			}
			b[r] -= f * b[col]
		}
	}
	copy(out[:], b[:])
	return out, true
}
