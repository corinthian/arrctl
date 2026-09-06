// Package cause is the shared vocabulary for why a request failed. It is
// extraction only: it names the cause and nothing else. No exit numbers, no
// error-code strings, no retry policy — each tool maps a Cause to its own codes
// under its own rules, and two tools may legitimately map the same cause
// differently. Nothing here imports outside the standard library, because this
// package is copied byte-for-byte into arrctl, traktctl and plexctl.
package cause

// Cause is the classified reason a request or a response failed.
type Cause int

const (
	// Unknown is the zero value and the fallback for a cause that could not
	// be classified. It is never a transport cause in disguise: a transport
	// failure that matches nothing specific is TransportOther.
	Unknown Cause = iota
	Timeout
	DNS
	TLS
	Refused
	TransportOther
	Oversize
	Decode
	Cancelled
)

var causeNames = [...]string{
	Unknown:        "unknown",
	Timeout:        "timeout",
	DNS:            "dns",
	TLS:            "tls",
	Refused:        "refused",
	TransportOther: "transport",
	Oversize:       "oversize",
	Decode:         "decode",
	Cancelled:      "cancelled",
}

// String names the cause. A value outside the enum reads as "unknown" rather
// than as a bare number, so a message built from it is never nonsense.
func (c Cause) String() string {
	if c < 0 || int(c) >= len(causeNames) {
		return causeNames[Unknown]
	}
	return causeNames[c]
}

// Coded is implemented by an error that already knows its own cause, so a
// wrapper can carry a classification the classifier could not have derived
// from the underlying error alone.
type Coded interface{ Cause() Cause }
