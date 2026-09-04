package config

import (
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path atomically and mode 0600: a temp file
// is created in path's directory (so the rename below is same-filesystem),
// written, then renamed over the destination. Rename replaces path directly
// -- if path is a symlink, the symlink itself is replaced rather than
// writing through it to whatever it points at.
func AtomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
