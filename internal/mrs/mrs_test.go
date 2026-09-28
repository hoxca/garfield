package mrs

import (
	"testing"
)

func makeTestImage(width, height int) [][]float64 {
	img := make([][]float64, height)
	for i := range img {
		img[i] = make([]float64, width)
		for j := range img[i] {
			img[i][j] = float64(i*1000 + j)
		}
	}
	return img
}

func BenchmarkConvolve2D_100(b *testing.B) {
	img := makeTestImage(100, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Convolve2D(img, 1)
	}
}

func BenchmarkConvolve2D_500(b *testing.B) {
	img := makeTestImage(500, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Convolve2D(img, 1)
	}
}

func BenchmarkATrousTransform_500_5s(b *testing.B) {
	img := makeTestImage(500, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ATrousTransform(img, 5)
	}
}

func BenchmarkEstimateBackgroundMap_500(b *testing.B) {
	img := makeTestImage(500, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateBackgroundMap(img, 5)
	}
}

func BenchmarkEstimateBackgroundAndNoise_500(b *testing.B) {
	img := makeTestImage(500, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateBackgroundAndNoise(img, 5)
	}
}

func BenchmarkEstimateNoiseSigma_500(b *testing.B) {
	detail := make([][]float64, 500)
	for i := range detail {
		detail[i] = make([]float64, 500)
		for j := range detail[i] {
			detail[i][j] = float64(i*1000 + j)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateNoiseSigma(detail, ScaleNoiseFactors[0])
	}
}

func BenchmarkEstimateBackgroundAndNoise_2000(b *testing.B) {
	img := makeTestImage(2000, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateBackgroundAndNoise(img, 5)
	}
}

func BenchmarkEstimateBackgroundAndNoiseBinned_500(b *testing.B) {
	img := makeTestImage(500, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateBackgroundAndNoiseBinned(img, 5)
	}
}

func BenchmarkEstimateBackgroundAndNoiseBinned_2000(b *testing.B) {
	img := makeTestImage(2000, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateBackgroundAndNoiseBinned(img, 5)
	}
}
