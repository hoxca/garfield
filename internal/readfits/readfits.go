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
			matrix[i][j] = rawData[i*width+j]
		}
	}

	return matrix, nil
}
