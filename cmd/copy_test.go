package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFileAtomicWritesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.FIT")
	dst := filepath.Join(dir, "dst.FIT")
	content := bytes.Repeat([]byte("astronomy"), 100_000)

	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := copyFileAtomic(src, dst); err != nil {
		t.Fatalf("copyFileAtomic: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("destination has %d bytes, want the %d written", len(got), len(content))
	}
	assertNoTempFiles(t, dir)
}

func TestCopyFileAtomicOverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.FIT")
	dst := filepath.Join(dir, "dst.FIT")

	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(dst, []byte("old-and-longer"), 0o644); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	if err := copyFileAtomic(src, dst); err != nil {
		t.Fatalf("copyFileAtomic: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(got) != "new" {
		t.Errorf("destination = %q, want the source contents", got)
	}
	assertNoTempFiles(t, dir)
}

func TestCopyFileAtomicCleansUpAfterReadFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory opens successfully but cannot be read as a stream, so the
	// failure happens after the temporary file exists: exactly the path that
	// must not leave a partial file behind.
	src := filepath.Join(dir, "source-dir")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	dst := filepath.Join(dir, "dst.FIT")

	if err := copyFileAtomic(src, dst); err == nil {
		t.Fatal("copyFileAtomic succeeded reading a directory")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("destination exists after a failed copy (stat err: %v)", err)
	}
	assertNoTempFiles(t, dir)
}

func TestCopyFileAtomicMissingSourceLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dst.FIT")

	if err := copyFileAtomic(filepath.Join(dir, "absent.FIT"), dst); err == nil {
		t.Fatal("copyFileAtomic succeeded on a missing source")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("destination exists after a failed copy (stat err: %v)", err)
	}
	assertNoTempFiles(t, dir)
}

// assertNoTempFiles fails when a copy left a temporary file in dir. Copies write
// ".<name>.tmp-<random>" beside the destination, so any dotfile on a successful
// or cleaned-up run is a leak.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}
