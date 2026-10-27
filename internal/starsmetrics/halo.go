package starsmetrics

import "sort"

// Halo measurement geometry, in pixels.
//
// The core is the star's inner profile just outside the saturated plateau; the
// wing is far enough out that the seeing-limited PSF has fallen away, and close
// enough in that the star's own scattered light is still measurable. The
// pedestal ring sits beyond the wing and estimates the local sky plus nebula,
// so a bright background is not mistaken for a halo.
const (
	haloCoreInner = 5.0
	haloCoreOuter = 15.0
	haloWingInner = 40.0
	haloWingOuter = 120.0
	haloPedInner  = 250.0
	haloPedOuter  = 350.0

	// haloTopStars is how many of the brightest stars contribute. The halo is
	// strong on a handful of bright stars and absent on the rest of the field;
	// averaging over many stars dilutes the signal until it disappears.
	haloTopStars = 10

	// HaloMinStars is the smallest sample that still gives a usable median. A
	// frame with fewer measured stars gets no halo value, and the cloud rule
	// skips it rather than deciding on noise.
	HaloMinStars = 3

	// haloCoreSignificance is how far the core must rise above the local
	// pedestal before the star is included. Below this the star has no
	// measurable profile at these radii and its ratio would be noise.
	haloCoreSignificance = 3.0
)

// MeasureHalo returns the median wing brightness of the brightest stars, in ADU
// above the local pedestal, and the number of stars it used.
//
// This is the observable behind cloud rejection. Thin cloud scatters starlight
// into a wide aureole: the wings of a bright star grow by a factor of several
// while the core and the PSF width barely move. Faint stars carry too little
// flux for the aureole to clear the noise, which is why only the brightest are
// used and why the measurement fails on a star that has no intrinsic halo.
//
// The value is absolute, not a ratio to the core. Normalising by the core
// compresses the signal, because the core annulus is itself inside the growing
// aureole; the frame-to-frame comparison is made against the session instead.
//
// skyLevel is the frame's median sky. It only ranks candidates: the pedestal is
// measured per star afterwards. Ranking by peak or by box flux instead would
// select the sharp saturated stars, whose halo is absent, and skip the bright
// haloed stars that carry the signal.
//
// stars must be the field's detections. Frames are measured against their own
// session, so target, filter and exposure drop out of the final statistic.
func MeasureHalo(pixels [][]float64, stars []StarMetrics, noise, skyLevel float64) (float64, int) {
	if noise <= 0 || len(pixels) == 0 {
		return 0, 0
	}
	height, width := len(pixels), len(pixels[0])
	margin := int(haloPedOuter)
	if height <= 2*margin || width <= 2*margin {
		return 0, 0
	}

	// Rank by the core annulus above the sky. This is cheap (a small ring) and
	// it is the quantity that separates a star with an aureole from one
	// without; peak brightness does not. Only the best candidates then pay for
	// the pedestal measurement.
	type candidate struct {
		star StarMetrics
		core float64
		wing float64
	}
	var ranked []candidate
	for _, s := range stars {
		if s.X < margin || s.Y < margin || s.X >= width-margin || s.Y >= height-margin {
			continue
		}
		ranked = append(ranked, candidate{star: s, core: medianAnnulus(pixels, s.X, s.Y, haloCoreInner, haloCoreOuter) - skyLevel})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].core > ranked[j].core })
	if len(ranked) > 4*haloTopStars {
		ranked = ranked[:4*haloTopStars]
	}

	var measured []candidate
	for _, c := range ranked {
		ped := medianAnnulus(pixels, c.star.X, c.star.Y, haloPedInner, haloPedOuter)
		core := medianAnnulus(pixels, c.star.X, c.star.Y, haloCoreInner, haloCoreOuter) - ped
		if core <= haloCoreSignificance*noise {
			continue
		}
		wing := medianAnnulus(pixels, c.star.X, c.star.Y, haloWingInner, haloWingOuter) - ped
		if wing <= 0 {
			continue
		}
		measured = append(measured, candidate{star: c.star, core: core, wing: wing})
		if len(measured) >= haloTopStars {
			break
		}
	}
	if len(measured) < HaloMinStars {
		return 0, 0
	}

	// The first haloTopStars that pass are used. The rank is a proxy for the
	// core, not a final ordering, but the median over a sample this size does
	// not depend on the order beyond which stars made the cut.
	wings := make([]float64, len(measured))
	for i, c := range measured {
		wings[i] = c.wing
	}
	return medianOfFloat(wings), len(wings)
}

// medianAnnulus returns the median pixel value in the annulus [inner, outer]
// around (cx, cy). The median rather than the mean: a neighbouring star or a
// hot column in the ring would drag a mean far off, while the sky and the
// broad halo move every pixel and survive.
func medianAnnulus(pixels [][]float64, cx, cy int, inner, outer float64) float64 {
	hi := int(outer)
	lo2 := inner * inner
	hi2 := outer * outer
	// The wide rings are sampled every second pixel: the sky and the halo are
	// smooth at those scales, and the full ring would allocate six figures of
	// floats per star.
	step := 1
	if outer >= haloWingInner {
		step = 2
	}
	values := make([]float64, 0, 2*hi*hi)
	for dy := -hi; dy <= hi; dy += step {
		for dx := -hi; dx <= hi; dx += step {
			d2 := float64(dx*dx + dy*dy)
			if d2 < lo2 || d2 > hi2 {
				continue
			}
			values = append(values, pixels[cy+dy][cx+dx])
		}
	}
	return medianOfFloat(values)
}

func medianOfFloat(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}
