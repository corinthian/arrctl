// Package atomicfile writes a file so a reader sees either the old content or
// the new one, never a half-written file.
//
// The mode is fixed at 0600 and there is no mode argument: everything these
// tools write atomically holds configuration, and a mode parameter is an
// invitation to widen it at one call site. Locking is the caller's job, and so
// is creating the directory — atomicfile never calls MkdirAll.
//
// It imports the standard library only, because it is copied byte-for-byte
// into traktctl and plexctl.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write writes data to path atomically at mode 0600.
//
// The temp file is created in the target's own directory so the rename is
// same-filesystem, chmodded before any bytes are written so the content is
// never briefly world-readable, and fsynced before the rename so a crash
// cannot leave a renamed-but-empty file. On any failure the temp is removed
// and path is left exactly as it was.
//
// If path is a symlink the rename replaces the symlink itself; the file it
// pointed at is untouched. That is deliberate — writing through the link would
// let a symlink planted by another process redirect the write.
//
// The parent directory is not fsynced after the rename: the contract is atomic
// visibility, not crash durability.
func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	// A no-op once the rename below succeeds, and the cleanup on every path
	// that does not reach it.
	defer os.Remove(name)

	fail := func(err error) error {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
