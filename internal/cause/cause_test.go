package cause

import "testing"

// The enum is the shared vocabulary three tools map to their own codes. A
// constant added without a name would render as an empty string in a message
// and be invisible in a test, so the count and the names are both asserted.
func TestCauseStringCoversEnum(t *testing.T) {
	want := []Cause{Unknown, Timeout, DNS, TLS, Refused, TransportOther, Oversize, Decode, Cancelled}
	if int(Cancelled)+1 != len(want) {
		t.Fatalf("the enum has %d values but %d are listed here", int(Cancelled)+1, len(want))
	}
	seen := map[string]Cause{}
	for _, c := range want {
		s := c.String()
		if s == "" {
			t.Errorf("Cause(%d) has no name", int(c))
			continue
		}
		if prev, dup := seen[s]; dup {
			t.Errorf("Cause(%d) and Cause(%d) share the name %q", int(prev), int(c), s)
		}
		seen[s] = c
	}
	if len(seen) != len(want) {
		t.Errorf("%d distinct names for %d causes", len(seen), len(want))
	}
	if got := Cause(len(want)).String(); got != "unknown" {
		t.Errorf("an out-of-range cause named %q, want the unknown fallback", got)
	}
}
