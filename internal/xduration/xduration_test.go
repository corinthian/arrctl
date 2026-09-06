package xduration

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseAcceptsIntegerSeconds(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"1", time.Second},
		{"30", 30 * time.Second},
		{"00030", 30 * time.Second},
		{"86400", 86400 * time.Second},
	} {
		got, err := Parse(tc.raw, "--timeout")
		if err != nil {
			t.Errorf("Parse(%q) = %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// Every rejection has to name the source, because the caller picks its error
// code from the source and the user needs to know which of three places to fix.
func TestParseRejects(t *testing.T) {
	for _, raw := range []string{
		"30s", "1m", "10.5", "1e3", "0", "-5", "86401", "",
		" 30 ", "NaN", "Inf", "+Inf", "1e-12", "abc", "3_0", "+30",
	} {
		got, err := Parse(raw, "$ARRCTL_TIMEOUT")
		if err == nil {
			t.Errorf("Parse(%q) was accepted as %v", raw, got)
			continue
		}
		var perr *Error
		if !errors.As(err, &perr) {
			t.Errorf("Parse(%q) returned %T, want *Error", raw, err)
			continue
		}
		if perr.Source != "$ARRCTL_TIMEOUT" {
			t.Errorf("Parse(%q) named source %q", raw, perr.Source)
		}
		if !strings.Contains(err.Error(), "$ARRCTL_TIMEOUT") {
			t.Errorf("Parse(%q) message does not name the source: %s", raw, err)
		}
	}
}

// The highest-priority present candidate is authoritative. If it is invalid the
// resolution fails; a valid lower one never rescues it.
func TestResolveFirstPresentWins(t *testing.T) {
	got, err := Resolve(30*time.Second,
		Candidate{"--timeout", ""},
		Candidate{"$ARRCTL_TIMEOUT", "abc"},
		Candidate{"config timeout (/x.toml)", "30"},
	)
	if err == nil {
		t.Fatalf("an invalid higher candidate was rescued, resolved to %v", got)
	}
	var perr *Error
	if !errors.As(err, &perr) || perr.Source != "$ARRCTL_TIMEOUT" {
		t.Fatalf("error = %v, want one naming $ARRCTL_TIMEOUT", err)
	}

	ok, err := Resolve(30*time.Second,
		Candidate{"--timeout", ""},
		Candidate{"$ARRCTL_TIMEOUT", "45"},
		Candidate{"config timeout (/x.toml)", "bogus"},
	)
	if err != nil {
		t.Fatalf("a valid higher candidate did not win: %v", err)
	}
	if ok != 45*time.Second {
		t.Fatalf("resolved %v, want 45s", ok)
	}
}

func TestResolveEmptyCandidatesUsesDefault(t *testing.T) {
	got, err := Resolve(10*time.Second, Candidate{"--timeout", ""}, Candidate{"$X", ""})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if got != 10*time.Second {
		t.Fatalf("resolved %v, want the 10s default", got)
	}
	if got, err = Resolve(10 * time.Second); err != nil || got != 10*time.Second {
		t.Fatalf("no candidates at all: %v %v", got, err)
	}
}
