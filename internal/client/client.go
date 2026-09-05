// Package client is the HTTP core: it prefixes /api/v3, places the API key in
// a header (or the query string), applies per-call query params, maps non-2xx
// responses and transport failures to typed errors with stable exit codes, and
// auto-paginates --all over the Servarr paged-response shape. All HTTP policy
// lives here, not in the per-command files.
package client

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
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/corinthian/arrctl/internal/output"
)

// APIPrefix is the v3 API path prefix. `/ping` and `/api` sit outside it.
const APIPrefix = "/api/v3"

// PageCap is the --all runaway guard.
const PageCap = 100

// KeyPlacement selects where the API key is sent.
type KeyPlacement string

const (
	KeyInHeader KeyPlacement = "header" // X-Api-Key (default)
	KeyInQuery  KeyPlacement = "query"  // ?apikey= (proxies that strip headers)
)

// ValidKeyPlacement reports whether s is a known --apikey-in value.
func ValidKeyPlacement(s string) bool {
	return s == string(KeyInHeader) || s == string(KeyInQuery)
}

// Client is a configured Radarr/Sonarr HTTP client.
type Client struct {
	http      *http.Client
	app       string
	baseURL   string
	apiKey    string
	placement KeyPlacement
	userAgent string
	keyHint   string
}

// Config configures a Client.
type Config struct {
	App       string // "radarr" | "sonarr"; lands in meta.app
	BaseURL   string // scheme://host[:port], no trailing slash
	APIKey    string
	Insecure  bool // skip TLS verification (self-signed/expired instance cert)
	Timeout   time.Duration
	Version   string // arrctl version, for User-Agent
	Placement KeyPlacement
	KeyHint   string // recovery line shown on an auth failure; never a key value
}

// New builds a Client.
func New(c Config) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Insecure {
		// Ruled 2026-09-04: the instance cert is self-signed by a private CA
		// and expired; the URL is a LAN name. Per-app, so the other app's
		// verification is untouched.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // deliberate, per-app
	}
	p := c.Placement
	if p == "" {
		p = KeyInHeader
	}
	return &Client{
		http:      &http.Client{Timeout: c.Timeout, Transport: tr, CheckRedirect: rejectCrossOriginRedirect},
		app:       c.App,
		baseURL:   strings.TrimRight(c.BaseURL, "/"),
		apiKey:    c.APIKey,
		placement: p,
		userAgent: "arrctl/" + c.Version,
		keyHint:   c.KeyHint,
	}
}

// rejectCrossOriginRedirect refuses to follow a redirect whose scheme or host
// differs from the first request's, so a misconfigured instance cannot retarget
// the API key to another origin.
func rejectCrossOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return fmt.Errorf("refusing cross-origin redirect: %s -> %s", first.Host, req.URL.Host)
	}
	return nil
}

// Options carries per-call request shaping.
type Options struct {
	Query url.Values  // command-set query params
	Body  interface{} // request body for POST/PUT
	All   bool        // auto-paginate a Servarr paged GET
	// NoPrefix sends the path as-is instead of under /api/v3 (for /ping, /api).
	NoPrefix bool
	// NoKey omits the API key entirely (/ping and /api need none).
	NoKey bool
}

// Result is a successful HTTP outcome.
type Result struct {
	Data       json.RawMessage
	Status     int
	Pagination *output.Pagination
	Endpoint   string
	DurationMS int64
}

// paged is the Servarr paged-response envelope. Unlike Trakt, paging lives in
// the body, not in X-Pagination-* headers.
type paged struct {
	Page          int               `json:"page"`
	PageSize      int               `json:"pageSize"`
	SortKey       string            `json:"sortKey"`
	SortDirection string            `json:"sortDirection"`
	TotalRecords  int               `json:"totalRecords"`
	Records       []json.RawMessage `json:"records"`
}

// decodePaged returns the paged envelope when body is one, else nil. A body
// with no `records` key (a bare array, a single object) is not paged.
func decodePaged(body []byte) *paged {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil
	}
	if _, ok := probe["records"]; !ok {
		return nil
	}
	var p paged
	if err := json.Unmarshal(body, &p); err != nil {
		return nil
	}
	return &p
}

// Do executes a request. The returned error is always *output.CLIError.
func (c *Client) Do(ctx context.Context, method, path string, opts Options) (*Result, *output.CLIError) {
	if !opts.NoKey && c.apiKey == "" {
		return nil, &output.CLIError{
			Code:     output.CodeBadConfig,
			Message:  "no api key for " + c.app,
			Hint:     c.keyHint,
			Exit:     output.ExitForCode(output.CodeBadConfig),
			App:      c.app,
			Endpoint: resolvePath(path, opts),
		}
	}
	if opts.All && method == http.MethodGet {
		return c.doAll(ctx, path, opts)
	}
	return c.doOnce(ctx, method, path, opts)
}

func (c *Client) doOnce(ctx context.Context, method, path string, opts Options) (*Result, *output.CLIError) {
	start := time.Now()
	endpoint := resolvePath(path, opts)
	req, cerr := c.buildRequest(ctx, method, path, opts)
	if cerr != nil {
		return nil, cerr
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.transportError(err, endpoint, time.Since(start))
	}
	defer resp.Body.Close()

	body, rerr := io.ReadAll(resp.Body)
	dur := time.Since(start).Milliseconds()
	if rerr != nil {
		return nil, &output.CLIError{
			Code: output.CodeParseError, Message: "reading response body: " + rerr.Error(),
			Exit: output.ExitTransport, App: c.app, Endpoint: endpoint, DurationMS: dur,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.mapHTTPError(resp, body, endpoint, dur)
	}

	res := &Result{Data: json.RawMessage(body), Status: resp.StatusCode, Endpoint: endpoint, DurationMS: dur}
	if p := decodePaged(body); p != nil {
		res.Pagination = &output.Pagination{Page: p.Page, PageSize: p.PageSize, TotalRecords: p.TotalRecords}
	}
	return res, nil
}

// doAll auto-paginates a Servarr paged GET, merging the per-page `records`
// arrays into one paged envelope. A non-paged body is returned untouched. The
// merged envelope reports the first page actually requested, not a constant 1:
// `--all --page 3` hands back a tail, and saying "page 1" would present it as
// the whole list from the top.
func (c *Client) doAll(ctx context.Context, path string, opts Options) (*Result, *output.CLIError) {
	start := time.Now()
	page := 1
	if v := opts.Query.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	firstPage := page
	pageSize := 100
	if v := opts.Query.Get("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pageSize = n
		}
	}

	merged := []json.RawMessage{}
	total := 0
	for {
		if page-firstPage+1 > PageCap {
			return nil, &output.CLIError{
				Code:    output.CodePaginationRunaway,
				Message: fmt.Sprintf("--all exceeded %d pages; narrow the query or drop --all", PageCap),
				Hint:    "use --page/--page-size to walk the list in bounded steps",
				Exit:    output.ExitForCode(output.CodePaginationRunaway),
				App:     c.app, Endpoint: resolvePath(path, opts),
			}
		}
		pageOpts := opts
		pageOpts.All = false
		q := url.Values{}
		for k, vs := range opts.Query {
			q[k] = append([]string(nil), vs...)
		}
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(pageSize))
		pageOpts.Query = q

		res, cerr := c.doOnce(ctx, http.MethodGet, path, pageOpts)
		if cerr != nil {
			return nil, cerr
		}
		p := decodePaged(res.Data)
		if p == nil {
			// Not a paged endpoint — hand back the single payload.
			res.DurationMS = time.Since(start).Milliseconds()
			return res, nil
		}
		merged = append(merged, p.Records...)
		total = p.TotalRecords

		// The page itself says whether there is more: an empty page or a
		// short one is the last. "Short" is measured against the page size
		// the response reports, falling back to the size requested, because
		// an instance may cap the page size below what was asked for.
		size := pageSize
		if p.PageSize > 0 {
			size = p.PageSize
		}
		if len(p.Records) == 0 || len(p.Records) < size {
			break
		}
		// totalRecords is a secondary bound only, and not every paged body
		// carries one — an absent total read as zero used to end the walk
		// after page one. The arithmetic has to count the pages that were
		// skipped when the walk started later than page 1: merged never
		// holds them.
		if total > 0 && (firstPage-1)*size+len(merged) >= total {
			break
		}
		page++
	}

	out, err := json.Marshal(map[string]interface{}{
		"page": firstPage, "pageSize": len(merged), "totalRecords": total, "records": merged,
	})
	if err != nil {
		return nil, &output.CLIError{
			Code: output.CodeParseError, Message: "merging pages: " + err.Error(),
			Exit: output.ExitInternal, App: c.app, Endpoint: resolvePath(path, opts),
		}
	}
	return &Result{
		Data:       out,
		Status:     http.StatusOK,
		Pagination: &output.Pagination{Page: firstPage, PageSize: len(merged), TotalRecords: total},
		Endpoint:   resolvePath(path, opts),
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// resolvePath applies the /api/v3 prefix unless the call opted out (/ping, /api).
func resolvePath(path string, opts Options) string {
	if opts.NoPrefix {
		return path
	}
	return APIPrefix + path
}

func (c *Client) buildRequest(ctx context.Context, method, path string, opts Options) (*http.Request, *output.CLIError) {
	full := resolvePath(path, opts)
	u := c.baseURL + full

	q := url.Values{}
	for k, vs := range opts.Query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	if !opts.NoKey && c.placement == KeyInQuery {
		q.Set("apikey", c.apiKey)
	}
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}

	var bodyReader io.Reader
	if opts.Body != nil {
		b, err := json.Marshal(opts.Body)
		if err != nil {
			return nil, &output.CLIError{
				Code: output.CodeParseError, Message: "encoding request body: " + err.Error(),
				Exit: output.ExitInternal, App: c.app, Endpoint: full,
			}
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, &output.CLIError{
			Code: output.CodeBadConfig, Message: "building request: " + err.Error(),
			Exit: output.ExitUser, App: c.app, Endpoint: full,
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if opts.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !opts.NoKey && c.placement == KeyInHeader {
		req.Header.Set("X-Api-Key", c.apiKey)
	}
	return req, nil
}

// transportError classifies a client-side failure into the TRANSPORT_* family.
// The message is scrubbed of the request URL, because under --apikey-in query
// Go's url.Error carries the full URL — key included.
func (c *Client) transportError(err error, path string, d time.Duration) *output.CLIError {
	e := &output.CLIError{
		Exit: output.ExitTransport, App: c.app, Endpoint: path, DurationMS: d.Milliseconds(),
	}
	var dnsErr *net.DNSError
	var certErr *x509.CertificateInvalidError
	var hostErr x509.HostnameError
	var authErr x509.UnknownAuthorityError
	var tlsErr *tls.CertificateVerificationError

	switch {
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		e.Code = output.CodeTransportTimeout
		e.Message = "request to " + c.app + " timed out"
		e.Hint = "raise --timeout, or check the instance is up"
	case errors.As(err, &dnsErr):
		e.Code = output.CodeTransportDNS
		e.Message = "cannot resolve the " + c.app + " host: " + dnsErr.Name
	case errors.As(err, &tlsErr), errors.As(err, &certErr), errors.As(err, &hostErr), errors.As(err, &authErr):
		e.Code = output.CodeTransportTLS
		e.Message = "TLS verification failed for " + c.app
		e.Hint = "if this instance uses a self-signed or expired certificate, set insecure = true under [" + c.app + "] in the config, or pass --insecure"
	case errors.Is(err, syscall.ECONNREFUSED):
		e.Code = output.CodeTransportRefused
		e.Message = "connection refused by " + c.app
	default:
		e.Code = output.CodeTransportRefused
		e.Message = "transport error talking to " + c.app + ": " + scrub(err)
	}
	return e
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// scrub renders a transport error without the request URL. url.Error.Error()
// embeds the full URL, which under --apikey-in query contains the API key.
func scrub(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// mapHTTPError converts a non-2xx response into a typed CLIError.
func (c *Client) mapHTTPError(resp *http.Response, body []byte, path string, dur int64) *output.CLIError {
	e := &output.CLIError{
		HTTPStatus: resp.StatusCode, Exit: output.ExitArr,
		App: c.app, Endpoint: path, DurationMS: dur,
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		e.Code = output.CodeArrAuthFailed
		e.Message = fmt.Sprintf("%s returned %d %s", c.app, resp.StatusCode, http.StatusText(resp.StatusCode))
		e.Hint = c.keyHint
	case resp.StatusCode == http.StatusNotFound:
		e.Code = output.CodeArrNotFound
		e.Message = fmt.Sprintf("%s has no such resource (HTTP 404)", c.app)
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity:
		e.Code = output.CodeArrValidation
		e.Message = fmt.Sprintf("%s rejected the request (HTTP %d)", c.app, resp.StatusCode)
		e.Hint = validationHint(body)
	default:
		e.Code = output.CodeArrError
		e.Message = fmt.Sprintf("%s returned HTTP %d", c.app, resp.StatusCode)
	}
	if e.Hint == "" {
		if snippet := strings.TrimSpace(string(body)); snippet != "" && len(snippet) < 300 {
			e.Hint = snippet
		}
	}
	return e
}

// validationHint pulls the human part out of Servarr's validation body, an
// array of {propertyName, errorMessage} objects.
func validationHint(body []byte) string {
	var rows []struct {
		PropertyName string `json:"propertyName"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		switch {
		case r.PropertyName != "" && r.ErrorMessage != "":
			parts = append(parts, r.PropertyName+": "+r.ErrorMessage)
		case r.ErrorMessage != "":
			parts = append(parts, r.ErrorMessage)
		}
	}
	return strings.Join(parts, "; ")
}
