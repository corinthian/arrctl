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
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/corinthian/arrctl/internal/cause"
)

// --- ReadBody ---

// closeCounter records Close calls so the "closed on every path" contract can
// be asserted rather than assumed.
type closeCounter struct {
	io.Reader
	closes int
}

func (c *closeCounter) Close() error { c.closes++; return nil }

func respBody(r io.Reader) (*http.Response, *closeCounter) {
	cc := &closeCounter{Reader: r}
	return &http.Response{Body: cc}, cc
}

func TestReadBodyExactLimitSucceeds(t *testing.T) {
	resp, _ := respBody(bytes.NewReader(bytes.Repeat([]byte("x"), 10)))
	b, err := ReadBody(resp, 10)
	if err != nil {
		t.Fatalf("a body of exactly the limit was rejected: %v", err)
	}
	if len(b) != 10 {
		t.Fatalf("read %d bytes, want 10", len(b))
	}
}

func TestReadBodyLimitPlusOneIsOversize(t *testing.T) {
	resp, _ := respBody(bytes.NewReader(bytes.Repeat([]byte("x"), 11)))
	if _, err := ReadBody(resp, 10); !errors.Is(err, ErrOversize) {
		t.Fatalf("err = %v, want ErrOversize", err)
	}
}

func TestReadBodyClosesOnEveryPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.Reader
	}{
		{"success", bytes.NewReader([]byte("ok"))},
		{"oversize", bytes.NewReader(bytes.Repeat([]byte("x"), 99))},
		{"io failure", iotest{err: errors.New("boom")}},
	} {
		resp, cc := respBody(tc.body)
		_, _ = ReadBody(resp, 10)
		if cc.closes != 1 {
			t.Errorf("%s: body closed %d times, want 1", tc.name, cc.closes)
		}
	}
}

type iotest struct{ err error }

func (r iotest) Read([]byte) (int, error) { return 0, r.err }

var errDisk = errors.New("sentinel read failure")

func TestReadBodyIOFailurePreservesCause(t *testing.T) {
	resp, _ := respBody(iotest{err: errDisk})
	_, err := ReadBody(resp, 100)
	if !errors.Is(err, errDisk) {
		t.Fatalf("err = %v, want it to wrap the sentinel", err)
	}
}

// --- DecodeOne ---

func TestDecodeOneRejectsTrailingGarbage(t *testing.T) {
	var v any
	if err := DecodeOne([]byte(`{"a":1} junk`), &v); err == nil {
		t.Fatal("trailing garbage was accepted")
	}
	// Trailing content that happens to parse is equally a decode error.
	if err := DecodeOne([]byte(`{"a":1} }`), &v); err == nil {
		t.Fatal("a trailing brace was accepted")
	}
}

func TestDecodeOneRejectsTwoValues(t *testing.T) {
	var v any
	if err := DecodeOne([]byte("{\"a\":1}\n{\"b\":2}\n"), &v); err == nil {
		t.Fatal("two JSON values were accepted")
	}
}

func TestDecodeOneAcceptsTrailingWhitespace(t *testing.T) {
	var v any
	if err := DecodeOne([]byte("{\"a\":1}\n\t \r\n"), &v); err != nil {
		t.Fatalf("trailing whitespace was rejected: %v", err)
	}
}

func TestDecodeOneEmptyIsNotAnError(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t "} {
		v := any("untouched")
		if err := DecodeOne([]byte(raw), &v); err != nil {
			t.Errorf("DecodeOne(%q) = %v, want nil", raw, err)
		}
		if v != "untouched" {
			t.Errorf("DecodeOne(%q) overwrote the destination with %v", raw, v)
		}
	}
}

func TestDecodeOneRejectsTruncated(t *testing.T) {
	var v any
	if err := DecodeOne([]byte(`{"a":`), &v); err == nil {
		t.Fatal("a truncated object was accepted")
	}
}

func TestDecodeOnePreserves2To53Plus1(t *testing.T) {
	var v map[string]any
	if err := DecodeOne([]byte(`{"id":9007199254740993}`), &v); err != nil {
		t.Fatalf("DecodeOne = %v", err)
	}
	n, ok := v["id"].(json.Number)
	if !ok {
		t.Fatalf("id decoded as %T, want json.Number (UseNumber is not set)", v["id"])
	}
	if n.String() != "9007199254740993" {
		t.Fatalf("id = %s, want 9007199254740993", n)
	}
}

// --- Redirects ---

func sameOriginClient(hops int) *http.Client {
	return NewClient(Options{Redirects: RedirectPolicy{SameOrigin: true, MaxHops: hops}})
}

func TestRedirectCrossOriginRefused(t *testing.T) {
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer dest.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dest.URL+"/elsewhere", http.StatusFound)
	}))
	defer src.Close()

	_, err := sameOriginClient(10).Get(src.URL)
	if err == nil {
		t.Fatal("a cross-origin redirect was followed")
	}
	want := "refused: cross-origin"
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "redirect to http://") {
		t.Fatalf("err = %v, want the cross-origin form", err)
	}
}

// Scheme and port are both part of the origin. Driving these through httptest
// would need a TLS server and a second listener; the policy function is the
// unit under test, so it is called directly.
func TestRedirectSchemeDowngradeRefused(t *testing.T) {
	check := sameOriginClient(10).CheckRedirect
	via := []*http.Request{{URL: mustURL(t, "https://host/a")}}
	err := check(&http.Request{URL: mustURL(t, "http://host/b")}, via)
	if err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("err = %v, want a cross-origin refusal for an https to http hop", err)
	}
}

func TestRedirectDifferentPortRefused(t *testing.T) {
	check := sameOriginClient(10).CheckRedirect
	via := []*http.Request{{URL: mustURL(t, "http://host:8989/a")}}
	err := check(&http.Request{URL: mustURL(t, "http://host:7878/b")}, via)
	if err == nil || !strings.Contains(err.Error(), "cross-origin") {
		t.Fatalf("err = %v, want a cross-origin refusal for a different port", err)
	}
}

// The cap is checked before the origin, so a same-origin chain that runs long
// reports the cap rather than being followed forever.
func TestRedirectOverCapReportsCap(t *testing.T) {
	var srv *httptest.Server
	n := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Redirect(w, r, srv.URL+fmt.Sprintf("/hop%d", n), http.StatusFound)
	}))
	defer srv.Close()

	_, err := sameOriginClient(10).Get(srv.URL)
	if err == nil {
		t.Fatal("an 11-hop same-origin chain was followed")
	}
	if !strings.Contains(err.Error(), "redirect refused after 10 hops") {
		t.Fatalf("err = %v, want the hop-cap message naming the cap", err)
	}
}

func TestRedirectRejectAll(t *testing.T) {
	check := NewClient(Options{Redirects: RedirectPolicy{RejectAll: true}}).CheckRedirect
	via := []*http.Request{{URL: mustURL(t, "http://host/a")}}
	if err := check(&http.Request{URL: mustURL(t, "http://host/b")}, via); err == nil {
		t.Fatal("reject-all followed a same-origin hop")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

// --- Classify ---

// A refusal arrives wrapped in a *url.Error. Without unwrapping it classifies
// as a generic transport failure and the "connection refused" hint is lost.
func TestClassifyUnwrapsURLError(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "http://host", Err: &net.OpError{
		Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
	}}
	if got := Classify(err); got != cause.Refused {
		t.Fatalf("Classify = %v, want Refused", got)
	}
}

func TestClassifyCancelledBeatsTimeout(t *testing.T) {
	if got := Classify(context.Canceled); got != cause.Cancelled {
		t.Fatalf("Classify(context.Canceled) = %v", got)
	}
	// Both apply at once: cancellation wins.
	both := &url.Error{Op: "Get", URL: "http://host", Err: cancelledTimeout{}}
	if got := Classify(both); got != cause.Cancelled {
		t.Fatalf("Classify = %v, want Cancelled to beat Timeout", got)
	}
}

// cancelledTimeout is both a net.Error reporting a timeout and a wrapper around
// context.Canceled, the ambiguous case 2.5 decides in cancellation's favour.
type cancelledTimeout struct{}

func (cancelledTimeout) Error() string   { return "cancelled while timing out" }
func (cancelledTimeout) Timeout() bool   { return true }
func (cancelledTimeout) Temporary() bool { return false }
func (cancelledTimeout) Unwrap() error   { return context.Canceled }

func TestClassifyDeadlineAndNetTimeoutBothTimeout(t *testing.T) {
	if got := Classify(context.DeadlineExceeded); got != cause.Timeout {
		t.Fatalf("DeadlineExceeded = %v, want Timeout", got)
	}
	nt := &url.Error{Op: "Get", URL: "http://host", Err: &net.OpError{
		Op: "dial", Err: netTimeout{},
	}}
	if got := Classify(nt); got != cause.Timeout {
		t.Fatalf("a net.Error timeout = %v, want Timeout", got)
	}
}

type netTimeout struct{}

func (netTimeout) Error() string   { return "i/o timeout" }
func (netTimeout) Timeout() bool   { return true }
func (netTimeout) Temporary() bool { return true }

func TestClassifyOversize(t *testing.T) {
	if got := Classify(fmt.Errorf("reading body: %w", ErrOversize)); got != cause.Oversize {
		t.Fatalf("Classify = %v, want Oversize", got)
	}
}

func TestClassifyDNSTLSAndDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want cause.Cause
	}{
		{"dns", &url.Error{Err: &net.DNSError{Err: "no such host", Name: "host"}}, cause.DNS},
		{"x509", &url.Error{Err: x509.UnknownAuthorityError{}}, cause.TLS},
		{"tls verification", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, cause.TLS},
		{"syntax", &json.SyntaxError{}, cause.Decode},
		{"unmarshal type", &json.UnmarshalTypeError{}, cause.Decode},
		{"unexpected eof", fmt.Errorf("decoding: %w", io.ErrUnexpectedEOF), cause.Decode},
		{"other", errors.New("something else"), cause.TransportOther},
		{"nil", nil, cause.Unknown},
	} {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewClientAppliesTimeoutAndTransport(t *testing.T) {
	tr := &http.Transport{}
	c := NewClient(Options{Timeout: 7 * time.Second, Transport: tr})
	if c.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.Transport != tr {
		t.Error("the supplied transport was not used")
	}
}
