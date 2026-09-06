package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteIsAtomicAndMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := Write(path, []byte("hello")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	// The rename is the only thing that ever appears in the directory.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("%d entries left in the directory, want only the target", len(ents))
	}
}

// A config path that is a symlink is replaced by the rename; the file it
// pointed at is untouched. This is deliberate, not incidental: writing through
// the link would let a symlink placed by someone else redirect a 0600 write.
func TestWriteReplacesSymlinkLeavingTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, []byte("new")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Errorf("the symlink's target was written through: %q", got)
	}
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t.Error("the path is still a symlink; the rename did not replace it")
	}
	if got, _ := os.ReadFile(link); string(got) != "new" {
		t.Errorf("content = %q, want the new bytes", got)
	}
}

// atomicfile never calls MkdirAll: creating the directory is the caller's
// decision, including its mode.
func TestWriteMissingDirectoryIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope", "config.toml")
	if err := Write(path, []byte("x")); err == nil {
		t.Fatal("a missing target directory was not an error")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Error("the directory was created")
	}
}

func TestWriteRemovesTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	// Renaming onto an existing directory fails, and the temp must not be
	// left behind when it does.
	path := filepath.Join(dir, "target")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("x")); err == nil {
		t.Fatal("renaming over a non-empty directory succeeded")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("%d entries, want only the directory: a temp file was left behind", len(ents))
	}
}

func TestWriteZeroLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.toml")
	if err := Write(path, nil); err != nil {
		t.Fatalf("Write = %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("a zero-length write produced no file: %v", err)
	}
	if st.Size() != 0 {
		t.Fatalf("size = %d, want 0", st.Size())
	}
}
