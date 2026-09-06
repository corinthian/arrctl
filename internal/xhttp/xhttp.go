// Package xhttp is the shared HTTP edge: the one http.Client constructor, the
// one bounded body read, the one strict JSON decode, and the classifier that
// turns a transport error into a cause.Cause.
//
// It holds no policy of its own. The redirect rules, the body bound and the
// mapping from a cause to an exit code are all the caller's; this package only
// enforces what it is handed. It imports the standard library and
// internal/cause, nothing else, because it is copied byte-for-byte into
// traktctl and plexctl.
package xhttp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/corinthian/arrctl/internal/cause"
)

// Doer is the subset of *http.Client a request path needs, so a caller can
// substitute a stub without standing up a server.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// RedirectPolicy describes which redirects a client will follow.
//
// RejectAll refuses every hop and wins over the other two fields. Otherwise
// MaxHops bounds the chain and SameOrigin requires each hop to keep the scheme
// and host of the original request. There is no loop detection: a chain that
// returns to a URL it already visited is bounded by MaxHops and reported as
// over-cap, which is the same outcome with a simpler rule.
type RedirectPolicy struct {
	SameOrigin bool
	MaxHops    int
	RejectAll  bool
}

// Options is everything NewClient needs. A nil Transport means the stdlib
// default; a zero Timeout means none.
type Options struct {
	Timeout   time.Duration
	Redirects RedirectPolicy
	Transport http.RoundTripper
}

// NewClient is the only http.Client constructor. Routing every client through
// it is what stops a new call site from quietly inheriting the stdlib's
// follow-anything redirect behaviour.
func NewClient(o Options) *http.Client {
	p := o.Redirects
	return &http.Client{
		Timeout:   o.Timeout,
		Transport: o.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if p.RejectAll {
				return fmt.Errorf("redirect to %s://%s refused: redirects are not followed",
					req.URL.Scheme, req.URL.Host)
			}
			// The cap is checked first, so a long same-origin chain is
			// reported as over-cap rather than followed indefinitely.
			if p.MaxHops > 0 && len(via) > p.MaxHops {
				return errors.New("redirect refused after " + strconv.Itoa(p.MaxHops) + " hops")
			}
			if p.SameOrigin && len(via) > 0 {
				first := via[0].URL
				if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
					return fmt.Errorf("redirect to %s://%s refused: cross-origin",
						req.URL.Scheme, req.URL.Host)
				}
			}
			return nil
		},
	}
}

// ErrOversize reports a response body larger than the caller's bound. The body
// is never truncated and never decoded: a partial body is worse than no body,
// because it can parse.
var ErrOversize = errors.New("response body exceeds the bound")

// ReadBody reads at most limit+1 bytes so oversize is detectable without
// holding the whole thing. Exactly limit bytes is a valid body. The response
// body is closed on every path, including oversize and I/O failure. A read
// failure is wrapped, so errors.Is and errors.As still reach the cause.
func ReadBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if int64(len(b)) > limit {
		return nil, ErrOversize
	}
	return b, nil
}

// DecodeOne decodes exactly one JSON value into into, with UseNumber set so
// integer literals survive inspection. Anything but whitespace after that value
// is a decode error, whether or not it parses — two values arriving where one
// was promised is a malformed response, not a stream.
//
// Empty and whitespace-only input is not an error: it is an empty body, which
// every caller renders its own way. into is left untouched in that case.
func DecodeOne(data []byte, into any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(into); err != nil {
		return err
	}
	var rest json.RawMessage
	if err := dec.Decode(&rest); err != io.EOF {
		if err != nil {
			return fmt.Errorf("unexpected content after the JSON value: %w", err)
		}
		return errors.New("unexpected second JSON value after the first")
	}
	return nil
}

// Classify names why a request or a response failed. It is extraction only:
// the caller maps the cause to its own code and exit.
func Classify(err error) cause.Cause {
	if err == nil {
		return cause.Unknown
	}
	// An error that already knows its own cause is authoritative; nothing
	// derived from the underlying error can be better informed.
	var coded cause.Coded
	if errors.As(err, &coded) {
		return coded.Cause()
	}
	// Cancellation is tested before any timeout. A request cancelled while a
	// deadline was also expiring is a cancellation: the caller's own act, and
	// never grounds for a retry hint.
	if errors.Is(err, context.Canceled) {
		return cause.Cancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return cause.Timeout
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return cause.Timeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return cause.DNS
	}
	if isTLS(err) {
		return cause.TLS
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return cause.Refused
	}
	if errors.Is(err, ErrOversize) {
		return cause.Oversize
	}
	var syn *json.SyntaxError
	var ute *json.UnmarshalTypeError
	if errors.As(err, &syn) || errors.As(err, &ute) || errors.Is(err, io.ErrUnexpectedEOF) {
		return cause.Decode
	}
	return cause.TransportOther
}

// isTLS covers the certificate family: the verification wrapper and the x509
// errors it can carry, each of which is a distinct type rather than a sentinel.
func isTLS(err error) bool {
	var verify *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var alert tls.RecordHeaderError
	return errors.As(err, &verify) ||
		errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid) ||
		errors.As(err, &alert)
}
