package starsmetrics

import (
	"math"
	"testing"
)

// haloField builds a frame with stars on a flat sky and a broad halo around
// each, returning the pixels and the star positions.
func haloField(width, height int, haloAmp float64) ([][]float64, []StarMetrics) {
	pixels := make([][]float64, height)
	for y := range pixels {
		pixels[y] = make([]float64, width)
		for x := range pixels[y] {
			pixels[y][x] = 1000
		}
	}
	centres := [][2]int{{400, 400}, {500, 400}, {600, 400}}
	stars := make([]StarMetrics, len(centres))
	for i, c := range centres {
		cx, cy := c[0], c[1]
		for dy := -8; dy <= 8; dy++ {
			for dx := -8; dx <= 8; dx++ {
				r2 := float64(dx*dx+dy*dy) / (2 * 1.5 * 1.5)
				pixels[cy+dy][cx+dx] += 3000 * math.Exp(-r2)
			}
		}
		for dy := -120; dy <= 120; dy++ {
			for dx := -120; dx <= 120; dx++ {
				x, y := cx+dx, cy+dy
				if x < 0 || y < 0 || x >= width || y >= height {
					continue
				}
				r2 := float64(dx*dx+dy*dy) / (2 * 40 * 40)
				pixels[y][x] += haloAmp * math.Exp(-r2)
			}
		}
		stars[i] = StarMetrics{X: cx, Y: cy, TotalFlux: 1e6, Signal: 3000}
	}
	return pixels, stars
}

func TestMeasureHaloGrowsWithTheAureole(t *testing.T) {
	pixels, stars := haloField(1000, 1000, 100)
	halo, n := MeasureHalo(pixels, stars, 1, 1000)
	if n != len(stars) {
		t.Fatalf("used %d stars, want %d", n, len(stars))
	}
	if halo <= 0 {
		t.Fatalf("halo = %v, want a positive wing brightness", halo)
	}

	brighter, _ := haloField(1000, 1000, 300)
	stronger, _ := MeasureHalo(brighter, stars, 1, 1000)
	if stronger <= halo {
		t.Errorf("halo = %.1f with a 3x aureole, want more than %.1f", stronger, halo)
	}
}

func TestMeasureHaloNeedsEnoughStars(t *testing.T) {
	pixels, stars := haloField(1000, 1000, 100)
	halo, n := MeasureHalo(pixels, stars[:2], 1, 1000)
	if n != 0 || halo != 0 {
		t.Errorf("MeasureHalo with two stars = (%v, %d), want (0, 0)", halo, n)
	}
}

// TestMeasureHaloRejectsStarsTooTightToTheEdge guards the pedestal ring: a
// star nearer than 350 px to an edge has no complete ring, and including it
// would compare a partial annulus with a full one.
func TestMeasureHaloRejectsStarsTooTightToTheEdge(t *testing.T) {
	pixels, stars := haloField(1000, 1000, 100)
	for i := range stars {
		stars[i].X = 200
	}
	halo, n := MeasureHalo(pixels, stars, 1, 1000)
	if n != 0 || halo != 0 {
		t.Errorf("MeasureHalo near the edge = (%v, %d), want (0, 0)", halo, n)
	}
}
