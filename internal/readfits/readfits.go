package readfits

import (
	"fmt"
	"os"

	"github.com/astrogo/fitsio"
)

// ReadFITSImage opens a FITS file and converts its primary ImageHDU to a 2D float64 slice matrix
func ReadFITSImage(filename string) ([][]float64, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("unable to open file: %w", err)
	}
	defer f.Close()

	r, err := fitsio.Open(f)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize FITS reader: %w", err)
	}
	defer r.Close()

	hdu := r.HDU(0)
	img, ok := hdu.(fitsio.Image)
	if !ok {
		return nil, fmt.Errorf("primary HDU is not an image layer")
	}

	// BSCALE/BZERO place the stored integers on the physical scale. The common
	// case is BITPIX=16 with BZERO=32768, the standard way to store unsigned
	// 16-bit data; without this, physical ADU read back offset by 32768 and
	// negative. All the pipeline's existing consumers are relative and are
	// unaffected either way, but an absolute metric -- a sky level, a saturation
	// check -- needs the decoded values to mean something.
	bscale, bzero := 1.0, 0.0
	if card := hdu.Header().Get("BSCALE"); card != nil {
		if v, ok := numericCard(card.Value); ok {
			bscale = v
		}
	}
	if card := hdu.Header().Get("BZERO"); card != nil {
		if v, ok := numericCard(card.Value); ok {
			bzero = v
		}
	}

	axes := hdu.Header().Axes()
	if len(axes) < 2 {
		return nil, fmt.Errorf("expected at least a 2D image matrix, got dimensions: %d", len(axes))
	}
	width := axes[0]
	height := axes[1]
	bitpix := hdu.Header().Bitpix()

	var rawData []float64
	switch bitpix {
	case -64:
		buf := make([]float64, width*height)
		if err := img.Read(&buf); err != nil {
			return nil, fmt.Errorf("unable to read image data: %w", err)
		}
		rawData = buf
	case -32:
		buf := make([]float32, width*height)
		if err := img.Read(&buf); err != nil {
			return nil, fmt.Errorf("unable to read image data: %w", err)
		}
		rawData = make([]float64, len(buf))
		for i, v := range buf {
			rawData[i] = float64(v)
		}
	case 32:
		buf := make([]int32, width*height)
		if err := img.Read(&buf); err != nil {
			return nil, fmt.Errorf("unable to read image data: %w", err)
		}
		rawData = make([]float64, len(buf))
		for i, v := range buf {
			rawData[i] = float64(v)
		}
	case 16:
		buf := make([]int16, width*height)
		if err := img.Read(&buf); err != nil {
			return nil, fmt.Errorf("unable to read image data: %w", err)
		}
		rawData = make([]float64, len(buf))
		for i, v := range buf {
			rawData[i] = float64(v)
		}
	case 8:
		buf := make([]uint8, width*height)
		if err := img.Read(&buf); err != nil {
			return nil, fmt.Errorf("unable to read image data: %w", err)
		}
		rawData = make([]float64, len(buf))
		for i, v := range buf {
			rawData[i] = float64(v)
		}
	default:
		return nil, fmt.Errorf("unsupported BITPIX value: %d", bitpix)
	}

	matrix := make([][]float64, height)
	for i := range matrix {
		matrix[i] = make([]float64, width)
		for j := 0; j < width; j++ {
			matrix[i][j] = bzero + bscale*rawData[i*width+j]
		}
	}

	return matrix, nil
}

// numericCard converts the integer and floating-point types a FITS header card
// can hold to float64. Anything else leaves the default in place rather than
// guessing.
func numericCard(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	}
	return 0, false
}
