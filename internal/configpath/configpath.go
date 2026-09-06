// Package configpath decides which config file a tool should read. It does
// path selection and nothing else: reading, parsing and repair all stay with
// the caller, so a tolerant command can forgive an unusable path without also
// forgiving a file whose contents are wrong.
//
// The rule is that an explicit source is authoritative and never falls back.
// Silently reading a different file than the caller named is the worst outcome
// available, because the wrong instance answers and nothing says so. Only the
// default path is allowed to be absent.
//
// It imports the standard library only, because it is copied byte-for-byte
// into traktctl.
package configpath

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrConfigPath is the sentinel for a path that cannot be used. It is never
// wrapped around a parse failure: a file that exists and is readable but holds
// nonsense is a content problem, and the two are forgiven differently.
var ErrConfigPath = errors.New("config path unusable")

// ErrIsDirectory distinguishes a directory from any other stat failure, so a
// caller can say "is a directory, not a file" rather than surfacing a raw
// EISDIR from a later read. It wraps ErrConfigPath, so a caller that only cares
// that the path is unusable needs one check.
var ErrIsDirectory = fmt.Errorf("%w: path is a directory, not a file", ErrConfigPath)

// Resolve picks the config file from, in order, explicit, $envKey and
// defaultPath, and reports whether the winner was named by the caller.
//
// A value that is empty or whitespace at either explicit source counts as
// unset and falls through to the next candidate; a value that is present is
// authoritative and never does. A path that cannot be used is an error wrapping
// ErrConfigPath, with the underlying *fs.PathError left reachable through
// errors.As so the caller can build its own diagnostic.
//
// The one forgiveness is a default path that does not exist: that returns
// ("", false, nil), because every setting also has a flag and an environment
// variable. A default path that exists but is unusable is still an error.
func Resolve(explicit, envKey, defaultPath string) (string, bool, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		return check(p, true, false)
	}
	if p := strings.TrimSpace(os.Getenv(envKey)); p != "" {
		return check(p, true, false)
	}
	return check(defaultPath, false, true)
}

// check stats path and applies the forgiveness rule. forgiveMissing is set for
// the default path only.
func check(path string, explicit, forgiveMissing bool) (string, bool, error) {
	st, err := os.Stat(path)
	switch {
	case err != nil:
		if forgiveMissing && errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", explicit, fmt.Errorf("%w: %w", ErrConfigPath, err)
	case st.IsDir():
		return "", explicit, ErrIsDirectory
	}
	return path, explicit, nil
}
