// Package xduration is the one timeout parser the three Subtrakt tools share.
//
// The accepted grammar is deliberately narrow: a decimal integer number of
// seconds, base 10, no sign, no separators, no unit suffix, in the closed range
// 1 to 86400. Go duration strings ("30s"), floats and non-finite words are all
// rejected, and nothing is trimmed — a value arriving with whitespace around it
// is a quoting bug, and rejecting it surfaces the bug instead of hiding it.
//
// Resolution never falls through silently. The highest-priority candidate that
// is present is authoritative, and if it is invalid the resolution fails; a
// lower candidate is never consulted to rescue it. Every rejection names the
// source so the caller can choose an error code — the package itself names no
// tool's codes.
//
// Nothing here imports outside the standard library; the package is copied
// byte-for-byte into traktctl and plexctl.
package xduration

import (
	"strconv"
	"time"
)

// Bounds of the accepted range, in seconds.
const (
	MinSeconds = 1
	MaxSeconds = 86400
)

// Candidate is one place a timeout could have come from. Value is the raw text
// exactly as it arrived; an empty Value means the source is unset.
type Candidate struct{ Source, Value string }

// Error is a rejected timeout value. It carries the source so the caller can
// map it to a code — a flag is usually a usage error while an environment
// variable or a config file is a config error.
type Error struct{ Source, Value, Reason string }

func (e *Error) Error() string {
	return "invalid " + e.Source + " value " + strconv.Quote(e.Value) + ": " + e.Reason
}

// Parse converts raw into a duration under the integer-seconds grammar,
// attributing any failure to source.
func Parse(raw, source string) (time.Duration, error) {
	fail := func(reason string) (time.Duration, error) {
		return 0, &Error{Source: source, Value: raw, Reason: reason}
	}
	if raw == "" {
		return fail("expected a whole number of seconds, got an empty value")
	}
	// Digits only. strconv.Atoi would accept a leading sign, and a sign has
	// no meaning here: a negative timeout is nonsense and "+30" is a typo.
	for _, r := range raw {
		if r < '0' || r > '9' {
			return fail("expected a whole number of seconds between " +
				strconv.Itoa(MinSeconds) + " and " + strconv.Itoa(MaxSeconds) +
				", with no sign, separators or unit suffix")
		}
	}
	// Leading zeros are fine; only non-digits are not. Atoi still rejects a
	// literal too long to fit an int.
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fail("the number is out of range")
	}
	if n < MinSeconds || n > MaxSeconds {
		return fail("out of range: expected " + strconv.Itoa(MinSeconds) +
			" to " + strconv.Itoa(MaxSeconds) + " seconds")
	}
	return time.Duration(n) * time.Second, nil
}

// Resolve walks candidates in priority order and returns the first present one
// parsed. A candidate with an empty Value is unset and is skipped. When every
// candidate is unset, def is returned. An invalid present candidate is an
// error: resolution stops there rather than trying the next one.
func Resolve(def time.Duration, candidates ...Candidate) (time.Duration, error) {
	for _, c := range candidates {
		if c.Value == "" {
			continue
		}
		return Parse(c.Value, c.Source)
	}
	return def, nil
}
