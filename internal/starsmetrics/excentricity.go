/* Package starsmetrics compute star metrics */
package starsmetrics

import (
	"math"
	"sort"
)

type StarMetrics struct {
	X            int
	Y            int
	FWHM         float64
	Signal       float64
	TotalFlux    float64
	Eccentricity float64
}

func LocalBackground(pixels [][]float64, cx, cy int) float64 {
	const innerR = 5
	const outerR = 8
	height := len(pixels)
	width := len(pixels[0])

	var vals []float64
	for dy := -outerR; dy <= outerR; dy++ {
		y := cy + dy
		if y < 0 || y >= height {
			continue
		}
		for dx := -outerR; dx <= outerR; dx++ {
			x := cx + dx
			if x < 0 || x >= width {
				continue
			}
			r2 := dx*dx + dy*dy
			if r2 >= innerR*innerR && r2 <= outerR*outerR {
				vals = append(vals, pixels[y][x])
			}
		}
	}

	if len(vals) == 0 {
		return 0
	}

	sort.Float64s(vals)
	return vals[len(vals)/2]
}

func CalculateEccentricity(pixels, bgMap [][]float64, cx, cy, radius int) (float64, bool) {
	var m00, m10, m01, m20, m02, m11 float64
	height := len(pixels)
	width := len(pixels[0])
	useBgMap := len(bgMap) == height && len(bgMap) > 0 && len(bgMap[0]) == width

	for y := cy - radius; y <= cy+radius; y++ {
		if y < 0 || y >= height {
			continue
		}
		for x := cx - radius; x <= cx+radius; x++ {
			if x < 0 || x >= width {
				continue
			}
			bg := 0.0
			if useBgMap {
				bg = bgMap[y][x]
			}
			intensity := pixels[y][x] - bg
			if intensity < 0 {
				intensity = 0
			}
			m00 += intensity
			m10 += float64(x) * intensity
			m01 += float64(y) * intensity
		}
	}

	if m00 <= 0 {
		return 0, false
	}
	xCenter := m10 / m00
	yCenter := m01 / m00

	for y := cy - radius; y <= cy+radius; y++ {
		if y < 0 || y >= height {
			continue
		}
		dy := float64(y) - yCenter
		for x := cx - radius; x <= cx+radius; x++ {
			if x < 0 || x >= width {
				continue
			}
			dx := float64(x) - xCenter
			bg := 0.0
			if useBgMap {
				bg = bgMap[y][x]
			}
			intensity := pixels[y][x] - bg
			if intensity < 0 {
				intensity = 0
			}
			m20 += dx * dx * intensity
			m02 += dy * dy * intensity
			m11 += dx * dy * intensity
		}
	}

	mu20 := m20 / m00
	mu02 := m02 / m00
	mu11 := m11 / m00

	delta := mu20 - mu02
	term := math.Sqrt(delta*delta + 4.0*mu11*mu11)

	a2 := 2.0 * (mu20 + mu02 + term)
	b2 := 2.0 * (mu20 + mu02 - term)

	if a2 <= 0 || b2 <= 0 || b2 > a2 {
		return 0, false
	}

	eccentricity := math.Sqrt(1.0 - (b2 / a2))
	return eccentricity, true
}

func IsAbsolutePeak(pixels [][]float64, cx, cy int) bool {
	c := pixels[cy][cx]
	return c > pixels[cy][cx-1] && c > pixels[cy][cx+1] && c > pixels[cy-1][cx] && c > pixels[cy+1][cx]
}

func CalculateWidth(pixels, bgMap [][]float64, cx, cy, radius int) (float64, bool) {
	height := len(pixels)
	width := len(pixels[0])

	centerVal := pixels[cy][cx] - bgMap[cy][cx]
	if centerVal <= 0 {
		return 0, false
	}
	halfMax := centerVal / 2.0

	var sumRadius float64
	var countDirs float64

	leftFound := false
	rightFound := false
	for r := 1; r <= radius; r++ {
		if cx-r >= 0 && !leftFound {
			val := pixels[cy][cx-r] - bgMap[cy][cx-r]
			if val <= halfMax {
				prevVal := pixels[cy][cx-r+1] - bgMap[cy][cx-r+1]
				if prevVal > halfMax {
					frac := (halfMax - val) / (prevVal - val)
					sumRadius += float64(r) - frac
				} else {
					sumRadius += float64(r)
				}
				leftFound = true
			}
		}
		if cx+r < width && !rightFound {
			val := pixels[cy][cx+r] - bgMap[cy][cx+r]
			if val <= halfMax {
				prevVal := pixels[cy][cx+r-1] - bgMap[cy][cx+r-1]
				if prevVal > halfMax {
					frac := (halfMax - val) / (prevVal - val)
					sumRadius += float64(r) - frac
				} else {
					sumRadius += float64(r)
				}
				rightFound = true
			}
		}
	}
	if leftFound && rightFound {
		countDirs++
	}

	topFound := false
	bottomFound := false
	for r := 1; r <= radius; r++ {
		if cy-r >= 0 && !topFound {
			val := pixels[cy-r][cx] - bgMap[cy-r][cx]
			if val <= halfMax {
				prevVal := pixels[cy-r+1][cx] - bgMap[cy-r+1][cx]
				if prevVal > halfMax {
					frac := (halfMax - val) / (prevVal - val)
					sumRadius += float64(r) - frac
				} else {
					sumRadius += float64(r)
				}
				topFound = true
			}
		}
		if cy+r < height && !bottomFound {
			val := pixels[cy+r][cx] - bgMap[cy+r][cx]
			if val <= halfMax {
				prevVal := pixels[cy+r-1][cx] - bgMap[cy+r-1][cx]
				if prevVal > halfMax {
					frac := (halfMax - val) / (prevVal - val)
					sumRadius += float64(r) - frac
				} else {
					sumRadius += float64(r)
				}
				bottomFound = true
			}
		}
	}
	if topFound && bottomFound {
		countDirs++
	}

	if countDirs == 0 {
		return 0, false
	}

	fwhm := sumRadius / countDirs
	return fwhm, true
}

func FindStars(pixels [][]float64, bgMap [][]float64, noise float64) []StarMetrics {
	const maxStars = 12000
	var fieldStars []StarMetrics
	halfBox := 10
	height := len(pixels)
	width := len(pixels[0])

	skip := make([][]bool, height)
	for i := range skip {
		skip[i] = make([]bool, width)
	}

	for y := halfBox; y < height-halfBox; y++ {
		for x := halfBox; x < width-halfBox; x++ {
			if skip[y][x] {
				continue
			}
			val := pixels[y][x]
			bg := bgMap[y][x]
			threshold := bg + 25.0*noise

			if val > threshold && IsAbsolutePeak(pixels, x, y) {
				lbg := LocalBackground(pixels, x, y)
				peakSignal := val - lbg
				if peakSignal <= 0 {
					continue
				}
				var totalSignal float64
				for dy := -3; dy <= 3; dy++ {
					for dx := -3; dx <= 3; dx++ {
						s := pixels[y+dy][x+dx] - lbg
						if s > 0 {
							totalSignal += s
						}
					}
				}
				if totalSignal > 0 && peakSignal/totalSignal > 0.95 {
					continue
				}

				fwhm, okFWHM := CalculateWidth(pixels, bgMap, x, y, halfBox)
				if !okFWHM || fwhm < 1.8 || fwhm > 7.0 {
					continue
				}

				var totalFlux float64
				for dy := -halfBox; dy <= halfBox; dy++ {
					sy := y + dy
					if sy < 0 || sy >= height {
						continue
					}
					for dx := -halfBox; dx <= halfBox; dx++ {
						sx := x + dx
						if sx < 0 || sx >= width {
							continue
						}
						s := pixels[sy][sx] - lbg
						if s > 0 {
							totalFlux += s
						}
					}
				}

				fieldStars = append(fieldStars, StarMetrics{
					X:         x,
					Y:         y,
					FWHM:      fwhm,
					Signal:    peakSignal,
					TotalFlux: totalFlux,
				})

				for dy := -halfBox; dy <= halfBox; dy++ {
					sy := y + dy
					if sy < 0 || sy >= height {
						continue
					}
					for dx := -halfBox; dx <= halfBox; dx++ {
						sx := x + dx
						if sx >= 0 && sx < width {
							skip[sy][sx] = true
						}
					}
				}

				if len(fieldStars) >= maxStars {
					return fieldStars
				}
			}
		}
	}
	return fieldStars
}
