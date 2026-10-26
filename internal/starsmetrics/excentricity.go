/* Package starsmetrics compute star metrics */
package starsmetrics

import (
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
