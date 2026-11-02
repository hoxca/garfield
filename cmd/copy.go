package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// copyBufferSize is the chunk size used when streaming a frame. Frames are
// around 120 MB, so copying them whole would spike memory across sessions.
const copyBufferSize = 1 << 20

// copyFileAtomic streams src into dst through a temporary file in dst's
// directory and renames it into place only after every byte is written and the
// file is synced and closed.
//
// Writing straight to dst looks simpler, but a crash, a full disk or an
// interrupt mid-copy leaves a truncated frame with the right name. A later run
// with --skip-existing then sees a destination that exists and leaves it alone,
// so the corruption survives silently. The rename is atomic within a
// filesystem, so the destination is either absent, the previous file, or the
// complete new one.
func copyFileAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("unable to open %q: %w", src, err)
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return fmt.Errorf("unable to create a temporary file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Any failure below must leave neither the temporary file nor a partial
	// destination behind. Renaming is the only path that keeps the file.
	discard := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := io.CopyBuffer(tmp, in, make([]byte, copyBufferSize)); err != nil {
		discard()
		return fmt.Errorf("copying %q to %q: %w", src, dst, err)
	}
	if err := tmp.Sync(); err != nil {
		discard()
		return fmt.Errorf("syncing %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("moving %q into place: %w", dst, err)
	}
	return nil
}
