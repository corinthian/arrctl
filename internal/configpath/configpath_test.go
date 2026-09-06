package configpath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

const envKey = "ARRCTL_CONFIG_TEST"

func TestResolveExplicitMissingIsErrConfigPath(t *testing.T) {
	t.Setenv(envKey, "")
	missing := filepath.Join(t.TempDir(), "nope.toml")
	got, explicit, err := Resolve(missing, envKey, "/dev/null")
	if err == nil {
		t.Fatalf("a missing explicit path resolved to %q", got)
	}
	if !errors.Is(err, ErrConfigPath) {
		t.Errorf("err = %v, want it to wrap ErrConfigPath", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want the stat cause reachable", err)
	}
	if !explicit {
		t.Error("explicit = false for a caller-named path")
	}
	var perr *fs.PathError
	if !errors.As(err, &perr) {
		t.Error("the *fs.PathError is not reachable; the caller cannot rebuild its own message")
	}
}

func TestResolveEnvMissingIsErrConfigPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")
	t.Setenv(envKey, missing)
	_, explicit, err := Resolve("", envKey, "/dev/null")
	if !errors.Is(err, ErrConfigPath) {
		t.Fatalf("err = %v, want ErrConfigPath", err)
	}
	if !explicit {
		t.Error("explicit = false for a path named by the environment")
	}
}

func TestResolveExplicitDirectoryIsErrConfigPath(t *testing.T) {
	t.Setenv(envKey, "")
	dir := t.TempDir()
	_, _, err := Resolve(dir, envKey, "/dev/null")
	if !errors.Is(err, ErrConfigPath) {
		t.Fatalf("err = %v, want ErrConfigPath", err)
	}
	if !errors.Is(err, ErrIsDirectory) {
		t.Fatalf("err = %v, want it distinguishable as a directory", err)
	}
}

// An empty value at either explicit source is unset, not a path.
func TestResolveEmptyExplicitFallsThrough(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(def, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(dir, "env.toml")
	if err := os.WriteFile(env, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(envKey, "")
	got, explicit, err := Resolve("   ", envKey, def)
	if err != nil || got != def || explicit {
		t.Fatalf("empty flag and empty env: %q %v %v, want the default", got, explicit, err)
	}

	t.Setenv(envKey, "  "+env+" ")
	got, explicit, err = Resolve("", envKey, def)
	if err != nil || got != env || !explicit {
		t.Fatalf("empty flag, set env: %q %v %v", got, explicit, err)
	}
}

// The default file is allowed to be absent: every setting has a flag or an
// environment path.
func TestResolveDefaultMissingIsSilent(t *testing.T) {
	t.Setenv(envKey, "")
	def := filepath.Join(t.TempDir(), "absent.toml")
	got, explicit, err := Resolve("", envKey, def)
	if err != nil {
		t.Fatalf("a missing default was an error: %v", err)
	}
	if got != "" || explicit {
		t.Fatalf("path = %q explicit = %v, want no path at all", got, explicit)
	}
}

// Absent is forgiven; unusable is not. A directory at the default path means
// something is wrong that silence would hide.
func TestResolveDefaultDirectoryIsError(t *testing.T) {
	t.Setenv(envKey, "")
	_, _, err := Resolve("", envKey, t.TempDir())
	if !errors.Is(err, ErrConfigPath) || !errors.Is(err, ErrIsDirectory) {
		t.Fatalf("err = %v, want a directory error", err)
	}
}

// A dangling symlink gives ErrNotExist from Stat, so it is forgiven at the
// default path for exactly the same reason a missing file is.
func TestResolveDanglingSymlinkOnDefaultIsForgiven(t *testing.T) {
	t.Setenv(envKey, "")
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(filepath.Join(dir, "gone.toml"), link); err != nil {
		t.Fatal(err)
	}
	got, _, err := Resolve("", envKey, link)
	if err != nil {
		t.Fatalf("a dangling symlink at the default path was an error: %v", err)
	}
	if got != "" {
		t.Fatalf("path = %q, want none", got)
	}
	// The same link named explicitly is not forgiven.
	if _, _, err := Resolve(link, envKey, "/dev/null"); !errors.Is(err, ErrConfigPath) {
		t.Fatalf("an explicit dangling symlink was forgiven: %v", err)
	}
}

func TestResolveFollowsSymlinkToARealFile(t *testing.T) {
	t.Setenv(envKey, "")
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, explicit, err := Resolve(link, envKey, "/dev/null")
	if err != nil || got != link || !explicit {
		t.Fatalf("%q %v %v, want the link path back unchanged", got, explicit, err)
	}
}
