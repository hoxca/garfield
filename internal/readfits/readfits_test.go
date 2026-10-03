package readfits

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/astrogo/fitsio"
)

// writeFITS writes a FITS file containing a single primary image HDU and
// returns its path. Fixtures are generated at test time rather than committed
// as binaries, so the expected values stay visible in the test source.
//
// bitpix must be one the fitsio writer accepts; it rejects, for example,
// bitpix=-16 with a []int16 payload.
func writeFITS(t *testing.T, dir, name string, bitpix int, axes []int, data any, cards ...fitsio.Card) string {
	t.Helper()

	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}

	tf, err := fitsio.Create(f)
	if err != nil {
		f.Close()
		t.Fatalf("fitsio.Create %s: %v", name, err)
	}

	img := fitsio.NewImage(bitpix, axes)
	for _, c := range cards {
		if err := img.Header().Append(c); err != nil {
			tf.Close()
			f.Close()
			t.Fatalf("append card %s to %s: %v", c.Name, name, err)
		}
	}
	if err := img.Write(data); err != nil {
		tf.Close()
		f.Close()
		t.Fatalf("write pixels to %s: %v", name, err)
	}
	if err := tf.Write(img); err != nil {
		tf.Close()
		f.Close()
		t.Fatalf("write HDU to %s: %v", name, err)
	}
	if err := tf.Close(); err != nil {
		f.Close()
		t.Fatalf("close fitsio file %s: %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
	return path
}

// ramp16 builds an int16 ramp whose value equals its flat index, so a correct
// row-major read yields matrix[y][x] == y*width+x.
func ramp16(n int) []int16 {
	d := make([]int16, n)
	for i := range d {
		d[i] = int16(i)
	}
	return d
}

// payloadFor returns a pointer to a slice of the concrete Go type matching
// bitpix, plus the float64 values it is expected to decode back to.
//
// The values are distinct per pixel wherever the destination type can hold
// them, so a transposed or mis-strided read is caught. Integer types wrap once
// they run out of range, so shapes larger than the type allows are compared
// against the reference rather than against the flat index: the layout check
// stays valid, and the value check still catches a wrong dtype entirely.
//
// Bitpix -64 is the exception, since it can represent every index exactly.
func payloadFor(t *testing.T, bitpix, n int) (any, []float64) {
	t.Helper()
	switch bitpix {
	case -64:
		b := make([]float64, n)
		for i := range b {
			b[i] = float64(i)
		}
		ref := append([]float64(nil), b...)
		return &b, ref
	case -32:
		b := make([]float32, n)
		for i := range b {
			b[i] = float32(i)
		}
		ref := make([]float64, n)
		for i, v := range b {
			ref[i] = float64(v)
		}
		return &b, ref
	case 32:
		b := make([]int32, n)
		for i := range b {
			b[i] = int32(i)
		}
		ref := make([]float64, n)
		for i, v := range b {
			ref[i] = float64(v)
		}
		return &b, ref
	case 16:
		b := make([]int16, n)
		for i := range b {
			b[i] = int16(i)
		}
		ref := make([]float64, n)
		for i, v := range b {
			ref[i] = float64(v)
		}
		return &b, ref
	case 8:
		b := make([]uint8, n)
		for i := range b {
			b[i] = uint8(i)
		}
		ref := make([]float64, n)
		for i, v := range b {
			ref[i] = float64(v)
		}
		return &b, ref
	default:
		t.Fatalf("no payload helper for bitpix %d", bitpix)
		return nil, nil
	}
}

// TestReadFITSImageRoundTrip checks that every supported BITPIX is decoded into
// the same row-major 2D layout.
//
// ReadFITSImage allocates a width*height buffer and fills matrix[i][j] from
// rawData[i*width+j], so a distinct value per pixel pins both the byte order
// and the shape mapping. Getting these wrong is silent: the pipeline would run
// and produce plausible but transposed metrics.
func TestReadFITSImageRoundTrip(t *testing.T) {
	bitpixes := []int{-64, -32, 32, 16, 8}
	shapes := []struct {
		name   string
		width  int
		height int
	}{
		{"non-square wide", 5, 4},
		{"non-square tall", 3, 5},
		{"single pixel", 1, 1},
		{"single column", 1, 7},
		{"single row", 7, 1},
		{"square", 64, 64},
		{"multi-block", 300, 200},
	}

	for _, bitpix := range bitpixes {
		for _, shape := range shapes {
			name := "bitpix" + itoa(bitpix) + "_" + shape.name
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				n := shape.width * shape.height
				data, want := payloadFor(t, bitpix, n)
				path := writeFITS(t, dir, "img.fits", bitpix, []int{shape.width, shape.height}, data)

				m, err := ReadFITSImage(path)
				if err != nil {
					t.Fatalf("ReadFITSImage: %v", err)
				}

				if len(m) != shape.height {
					t.Fatalf("got %d rows, want %d", len(m), shape.height)
				}
				for y := range m {
					if len(m[y]) != shape.width {
						t.Fatalf("row %d has %d cols, want %d", y, len(m[y]), shape.width)
					}
					for x := range m[y] {
						if w := want[y*shape.width+x]; m[y][x] != w {
							t.Fatalf("m[%d][%d] = %v, want %v", y, x, m[y][x], w)
						}
					}
				}
			})
		}
	}
}

func itoa(v int) string {
	if v < 0 {
		return "m" + itoa(-v)
	}
	if v < 10 {
		return string(rune('0' + v))
	}
	return itoa(v/10) + string(rune('0'+v%10))
}

// TestReadFITSImageAxisOrder confirms axes[0] is the width and axes[1] the
// height, which is what the rawData[i*width+j] indexing assumes.
func TestReadFITSImageAxisOrder(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name          string
		width, height int
	}{
		{"5x4", 5, 4},
		{"3x5", 3, 5},
		{"1x9", 1, 9},
		{"9x1", 9, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := ramp16(tc.width * tc.height)
			path := writeFITS(t, dir, tc.name+".fits", 16, []int{tc.width, tc.height}, &data)

			// Confirm the header records the axes the way FITS expects, so the
			// assertion below is testing NAXIS1=width rather than a coincidence
			// of how the fixture was written.
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			r, err := fitsio.Open(f)
			if err != nil {
				f.Close()
				t.Fatalf("fitsio.Open: %v", err)
			}
			hdr := r.HDU(0).Header()
			axes := hdr.Axes()
			r.Close()
			f.Close()

			if axes[0] != tc.width || axes[1] != tc.height {
				t.Fatalf("header axes = %v, want [%d %d]", axes, tc.width, tc.height)
			}

			m, err := ReadFITSImage(path)
			if err != nil {
				t.Fatalf("ReadFITSImage: %v", err)
			}
			if len(m) != tc.height || len(m[0]) != tc.width {
				t.Fatalf("got %dx%d, want %dx%d", len(m), len(m[0]), tc.height, tc.width)
			}
		})
	}
}

// TestReadFITSImageInt16Range checks the full signed 16-bit range, including
// the extremes where a truncation or an unsigned interpretation would show up.
func TestReadFITSImageInt16Range(t *testing.T) {
	dir := t.TempDir()

	data := []int16{-32768, -1000, -1, 0, 1, 1000, 32767}
	path := writeFITS(t, dir, "signed.fits", 16, []int{len(data), 1}, &data)

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}
	if len(m) != 1 || len(m[0]) != len(data) {
		t.Fatalf("got %d rows", len(m))
	}
	for i, want := range data {
		if m[0][i] != float64(want) {
			t.Errorf("m[0][%d] = %v, want %v", i, m[0][i], float64(want))
		}
	}
}

// TestReadFITSImageUint8Range checks the full unsigned 8-bit range. Values above
// 127 would become negative if the payload were read as signed.
func TestReadFITSImageUint8Range(t *testing.T) {
	dir := t.TempDir()

	const w, h = 16, 16
	data := make([]uint8, w*h)
	for i := range data {
		data[i] = uint8(i)
	}
	path := writeFITS(t, dir, "u8.fits", 8, []int{w, h}, &data)

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}
	for y := range m {
		for x := range m[y] {
			want := float64(y*w + x)
			if m[y][x] != want {
				t.Fatalf("m[%d][%d] = %v, want %v", y, x, m[y][x], want)
			}
		}
	}
	if m[15][15] != 255 {
		t.Errorf("m[15][15] = %v, want 255", m[15][15])
	}
}

// TestReadFITSImageFloat32Precision pins the widening from float32 to float64.
// The values below are the exact float32-to-float64 conversions, so a future
// change to the decode path shows up immediately.
func TestReadFITSImageFloat32Precision(t *testing.T) {
	dir := t.TempDir()

	data := []float32{
		1.0 / 3.0,
		16777216.0, // 2^24
		1e-45,      // smallest positive subnormal float32
		3.4e38,     // near float32 max
	}
	path := writeFITS(t, dir, "f32.fits", -32, []int{len(data), 1}, &data)

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}

	for i, want := range data {
		got := m[0][i]
		if got != float64(want) {
			t.Errorf("m[0][%d] = %.17g, want %.17g", i, got, float64(want))
		}
	}
	// Spot-check that the widening is a real float32 value rather than the
	// original float64 literal sneaking through.
	if math.Abs(m[0][0]-0.3333333432674408) > 1e-18 {
		t.Errorf("1/3 decoded as %.17g, want the float32 value 0.3333333432674408", m[0][0])
	}
}

// TestReadFITSImageFloat64Exact checks that BITPIX=-64 needs no conversion.
func TestReadFITSImageFloat64Exact(t *testing.T) {
	dir := t.TempDir()

	data := []float64{0, 1.0 / 3.0, math.Pi, -1e300, 1e-300}
	path := writeFITS(t, dir, "f64.fits", -64, []int{len(data), 1}, &data)

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}
	for i, want := range data {
		if m[0][i] != want {
			t.Errorf("m[0][%d] = %.17g, want %.17g", i, m[0][i], want)
		}
	}
}

// TestReadFITSImageIgnoresBScaleBZero records that the BSCALE/BZERO rescaling
// keywords are NOT applied.
//
// fitsio performs the rescaling in its Image() path, not in Read(), and
// ReadFITSImage goes through Read(). A file declaring BSCALE=2, BZERO=100
// therefore reads back as its raw stored integers.
//
// This matters for the real frames in images/, which all carry
// BITPIX=16 with BSCALE=1 and BZERO=32768 -- the standard convention for
// storing unsigned 16-bit data in a signed BITPIX. The master flat reads back
// with min=-32295 and mean=-16582, i.e. offset by 32768 from physical ADU.
// The pipeline appears to tolerate the offset because every consumer is
// relative (background subtraction, noise thresholds, flux differences), which
// is consistent with the measured agreement against the reference tool. It
// would still be wrong for any consumer that treats a pixel as an absolute
// ADU, such as a saturation check.
//
// If this test ever starts failing because the rescaling was added, that is an
// improvement: the decoded values would then be physically correct, and the
// absolute-value consumers would become trustworthy.
func TestReadFITSImageIgnoresBScaleBZero(t *testing.T) {
	dir := t.TempDir()

	data := ramp16(6)
	path := writeFITS(t, dir, "bscale.fits", 16, []int{3, 2}, &data,
		fitsio.Card{Name: "BSCALE", Value: 2.0},
		fitsio.Card{Name: "BZERO", Value: 100.0})

	// Confirm the keywords really are in the header, otherwise this test would
	// pass for the wrong reason.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r, err := fitsio.Open(f)
	if err != nil {
		f.Close()
		t.Fatalf("fitsio.Open: %v", err)
	}
	hdr := r.HDU(0).Header()
	hasScale := hdr.Get("BSCALE") != nil
	hasZero := hdr.Get("BZERO") != nil
	r.Close()
	f.Close()
	if !hasScale || !hasZero {
		t.Fatalf("fixture is missing keywords: BSCALE=%v BZERO=%v", hasScale, hasZero)
	}

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}

	// Raw stored integers, not 100/102/104/106/108/110.
	want := []float64{0, 1, 2, 3, 4, 5}
	for y := range m {
		for x := range m[y] {
			if w := want[y*3+x]; m[y][x] != w {
				t.Errorf("m[%d][%d] = %v, want %v (BSCALE/BZERO are not applied)", y, x, m[y][x], w)
			}
		}
	}
}

// TestReadFITSImageOnlyPrimaryHDU confirms only HDU 0 is read, even when the
// file carries further extensions.
func TestReadFITSImageOnlyPrimaryHDU(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.fits")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tf, err := fitsio.Create(f)
	if err != nil {
		f.Close()
		t.Fatalf("fitsio.Create: %v", err)
	}

	first := []int16{10, 20, 30, 40, 50, 60}
	hdu1 := fitsio.NewImage(16, []int{3, 2})
	if err := hdu1.Write(&first); err != nil {
		t.Fatalf("write HDU 0: %v", err)
	}
	if err := tf.Write(hdu1); err != nil {
		t.Fatalf("write HDU 0: %v", err)
	}

	second := []int16{70, 80, 90, 100, 110, 120}
	hdu2 := fitsio.NewImage(16, []int{3, 2})
	if err := hdu2.Write(&second); err != nil {
		t.Fatalf("write HDU 1: %v", err)
	}
	if err := tf.Write(hdu2); err != nil {
		t.Fatalf("write HDU 1: %v", err)
	}

	if err := tf.Close(); err != nil {
		t.Fatalf("close fitsio: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}
	// HDU 0 holds {10, 20, 30, 40, 50, 60}; HDU 1 holds {70, 80, ...}.
	// Reading the wrong HDU would surface as 70 or 80 here.
	want := []float64{10, 20, 30, 40, 50, 60}
	for y := range m {
		for x := range m[y] {
			if w := want[y*3+x]; m[y][x] != w {
				t.Errorf("m[%d][%d] = %v, want %v (only HDU 0 should be read)", y, x, m[y][x], w)
			}
		}
	}
}

// TestReadFITSImageErrors covers the paths that return an error rather than a
// matrix. Every case must return a nil matrix alongside the error, since
// processImage propagates the error and never inspects the result.
func TestReadFITSImageErrors(t *testing.T) {
	dir := t.TempDir()

	// A 1x1 image whose data segment has been truncated.
	truncatedSrc := writeFITS(t, dir, "full.fits", 16, []int{1, 1}, func() *[]int16 {
		d := []int16{7}
		return &d
	}())
	raw, err := os.ReadFile(truncatedSrc)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	truncated := filepath.Join(dir, "truncated.fits")
	if err := os.WriteFile(truncated, raw[:len(raw)-20], 0o644); err != nil {
		t.Fatalf("write truncated fixture: %v", err)
	}

	garbage := filepath.Join(dir, "garbage.fits")
	if err := os.WriteFile(garbage, []byte("this is definitely not a FITS file"), 0o644); err != nil {
		t.Fatalf("write garbage fixture: %v", err)
	}

	binary := filepath.Join(dir, "binary.fits")
	if err := os.WriteFile(binary, make([]byte, 5000), 0o644); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}

	int64data := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	badBitpix := writeFITS(t, dir, "b64.fits", 64, []int{4, 3}, &int64data)

	oneD := []int16{1, 2, 3, 4, 5}
	oneDim := writeFITS(t, dir, "1d.fits", 16, []int{5}, &oneD)

	tests := []struct {
		name string
		path string
		want string
	}{
		{"nonexistent file", filepath.Join(dir, "does-not-exist.fits"), "unable to open file"},
		{"path is a directory", dir, "unable to initialize FITS reader"},
		{"non-FITS text", garbage, "unable to initialize FITS reader"},
		{"random binary", binary, "unable to initialize FITS reader"},
		{"truncated data segment", truncated, "unable to initialize FITS reader"},
		{"unsupported bitpix 64", badBitpix, "unsupported BITPIX value: 64"},
		{"one-dimensional image", oneDim, "expected at least a 2D image matrix, got dimensions: 1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ReadFITSImage(tc.path)
			if err == nil {
				t.Fatalf("expected an error, got matrix %dx%d", len(m), len(m[0]))
			}
			if m != nil {
				t.Errorf("expected a nil matrix alongside the error, got %d rows", len(m))
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestReadFITSImageErrorWrapping pins the %w wrapping on the open failure.
// Callers can sensibly want to distinguish "file missing" from "file corrupt",
// and that is only possible while the error chain is preserved.
func TestReadFITSImageErrorWrapping(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.fits")

	_, err := ReadFITSImage(missing)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false, want true (err: %v)", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("errors.As(err, **fs.PathError) = false, want true (err: %v)", err)
	}
}

// TestReadFITSImageSingleRowAndColumnAreUsable checks the degenerate shapes the
// detector will actually receive as a slice, verifying they read back without
// error rather than only round-tripping in the table above.
//
// Note the panic paths ReadFITSImage does not cover: an entirely empty file, a
// header with no data segment, and a file whose primary HDU is a table all
// panic inside fitsio.File.HDU, because ReadFITSImage calls r.HDU(0) without
// checking the file contains any HDUs. A 3-D or 4-D cube panics in
// fitsio.imageHDU.Read, because ReadFITSImage allocates width*height while
// such an image holds width*height*depth elements. None are reachable from
// cmd/analyze.go, which only feeds it 2-D frames selected by extension, but
// they are real hazards for any other caller.
func TestReadFITSImageSingleRowAndColumnAreUsable(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name   string
		width  int
		height int
	}{
		{"single row", 6, 1},
		{"single column", 1, 6},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := ramp16(tc.width * tc.height)
			path := writeFITS(t, dir, "thin.fits", 16, []int{tc.width, tc.height}, &data)

			m, err := ReadFITSImage(path)
			if err != nil {
				t.Fatalf("ReadFITSImage: %v", err)
			}
			if len(m) != tc.height || len(m[0]) != tc.width {
				t.Fatalf("got %dx%d, want %dx%d", len(m), len(m[0]), tc.height, tc.width)
			}
			for y := range m {
				for x := range m[y] {
					if w := float64(y*tc.width + x); m[y][x] != w {
						t.Errorf("m[%d][%d] = %v, want %v", y, x, m[y][x], w)
					}
				}
			}
		})
	}
}

// TestReadFITSImageRepeatedReadsIsStable confirms repeated reads of the same
// file return equal matrices. The function holds no state, but this guards
// against a future refactor that caches a shared buffer.
func TestReadFITSImageRepeatedReadsIsStable(t *testing.T) {
	dir := t.TempDir()
	const w, h = 40, 30

	data := make([]float64, w*h)
	for i := range data {
		data[i] = float64(i%17) * 1.5
	}
	path := writeFITS(t, dir, "repeat.fits", -64, []int{w, h}, &data)

	first, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	for run := 0; run < 3; run++ {
		again, err := ReadFITSImage(path)
		if err != nil {
			t.Fatalf("read %d: %v", run, err)
		}
		for y := range first {
			for x := range first[y] {
				if again[y][x] != first[y][x] {
					t.Fatalf("run %d: m[%d][%d] = %v, first read had %v", run, y, x, again[y][x], first[y][x])
				}
			}
		}
	}
}

// TestReadFITSImageRowsAreIndependentSlices checks that each row is its own
// backing array rather than a window onto one shared buffer. Downstream code
// slices rows and hands them to the convolution workers; aliased rows would
// make concurrent writes race.
func TestReadFITSImageRowsAreIndependentSlices(t *testing.T) {
	dir := t.TempDir()
	const w, h = 8, 4

	data := ramp16(w * h)
	path := writeFITS(t, dir, "rows.fits", 16, []int{w, h}, &data)

	m, err := ReadFITSImage(path)
	if err != nil {
		t.Fatalf("ReadFITSImage: %v", err)
	}

	// Mutating one row must not affect any other.
	m[1][0] = -999
	for y := range m {
		for x := range m[y] {
			want := float64(y*w + x)
			if y == 1 && x == 0 {
				want = -999
			}
			if m[y][x] != want {
				t.Fatalf("after mutating row 1: m[%d][%d] = %v, want %v", y, x, m[y][x], want)
			}
		}
	}
}
