package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/arrctl/internal/output"
)

func newTestClient(t *testing.T, srv *httptest.Server, app string) *Client {
	t.Helper()
	return New(Config{
		App: app, BaseURL: srv.URL, APIKey: "test-key", Timeout: 5 * time.Second,
		Version: "test", KeyHint: "store the key in the Keychain",
	})
}

// The key goes in the X-Api-Key header by default, and every v3 call is
// prefixed with /api/v3.
func TestHeaderKeyAndPrefix(t *testing.T) {
	var gotPath, gotKey, gotQueryKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotQueryKey = r.URL.Path, r.Header.Get("X-Api-Key"), r.URL.Query().Get("apikey")
		w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "radarr")
	res, cerr := c.Do(context.Background(), http.MethodGet, "/system/status", Options{})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if gotPath != "/api/v3/system/status" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "test-key" {
		t.Errorf("X-Api-Key = %q", gotKey)
	}
	if gotQueryKey != "" {
		t.Errorf("the key leaked into the query string: %q", gotQueryKey)
	}
	if res.Endpoint != "/api/v3/system/status" {
		t.Errorf("meta endpoint = %q", res.Endpoint)
	}
}

// --apikey-in query moves the key to the query string for proxies that strip
// headers.
func TestQueryKeyPlacement(t *testing.T) {
	var gotQueryKey, gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQueryKey, gotHeader = r.URL.Query().Get("apikey"), r.Header.Get("X-Api-Key")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(Config{App: "radarr", BaseURL: srv.URL, APIKey: "test-key",
		Timeout: 5 * time.Second, Placement: KeyInQuery})
	if _, cerr := c.Do(context.Background(), http.MethodGet, "/health", Options{}); cerr != nil {
		t.Fatal(cerr)
	}
	if gotQueryKey != "test-key" {
		t.Errorf("apikey query = %q", gotQueryKey)
	}
	if gotHeader != "" {
		t.Errorf("the header was also sent: %q", gotHeader)
	}
}

// /ping opts out of both the prefix and the key.
func TestNoPrefixNoKey(t *testing.T) {
	var gotPath, gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("X-Api-Key")
		w.Write([]byte(`{"status":"OK"}`))
	}))
	defer srv.Close()

	c := New(Config{App: "radarr", BaseURL: srv.URL, Timeout: 5 * time.Second})
	if _, cerr := c.Do(context.Background(), http.MethodGet, "/ping",
		Options{NoPrefix: true, NoKey: true}); cerr != nil {
		t.Fatal(cerr)
	}
	if gotPath != "/ping" {
		t.Errorf("path = %q", gotPath)
	}
	if gotKey != "" {
		t.Errorf("a key was sent to /ping: %q", gotKey)
	}
}

// HTTP status maps to the closed ARR_ code set with the right exit class, and
// meta.app names which instance answered.
func TestHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
		exit   output.ExitCode
	}{
		{401, ``, output.CodeArrAuthFailed, output.ExitArr},
		{403, ``, output.CodeArrAuthFailed, output.ExitArr},
		{404, ``, output.CodeArrNotFound, output.ExitArr},
		{400, `[{"propertyName":"Path","errorMessage":"is not writable"}]`, output.CodeArrValidation, output.ExitArr},
		{422, ``, output.CodeArrValidation, output.ExitArr},
		{500, ``, output.CodeArrError, output.ExitArr},
		{503, ``, output.CodeArrError, output.ExitArr},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := newTestClient(t, srv, "sonarr")
			_, cerr := c.Do(context.Background(), http.MethodGet, "/queue", Options{})
			if cerr == nil {
				t.Fatal("a non-2xx was treated as success")
			}
			if cerr.Code != tc.code {
				t.Errorf("code = %s, want %s", cerr.Code, tc.code)
			}
			if cerr.Exit != tc.exit {
				t.Errorf("exit = %d, want %d", cerr.Exit, tc.exit)
			}
			if cerr.App != "sonarr" {
				t.Errorf("app = %q", cerr.App)
			}
			if cerr.HTTPStatus != tc.status {
				t.Errorf("http_status = %d", cerr.HTTPStatus)
			}
		})
	}
}

// A 401 hint names the Keychain recovery line and never a key value.
func TestAuthHintIsTheRecoveryLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "sonarr")
	_, cerr := c.Do(context.Background(), http.MethodGet, "/series", Options{})
	if cerr == nil || !strings.Contains(cerr.Hint, "Keychain") {
		t.Fatalf("hint = %q", cerr.Hint)
	}
	if strings.Contains(cerr.Hint, "test-key") || strings.Contains(cerr.Message, "test-key") {
		t.Error("the API key appeared in the error")
	}
}

// Servarr's validation body is surfaced as a readable hint.
func TestValidationHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`[{"propertyName":"Path","errorMessage":"is not writable"}]`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "radarr")
	_, cerr := c.Do(context.Background(), http.MethodGet, "/rootfolder", Options{})
	if cerr == nil || !strings.Contains(cerr.Hint, "Path: is not writable") {
		t.Fatalf("hint = %q", cerr.Hint)
	}
}

// Paging comes from the response body, not X-Pagination-* headers.
func TestPaginationFromBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"page":2,"pageSize":10,"totalRecords":154,"records":[{"id":1}]}`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "sonarr")
	res, cerr := c.Do(context.Background(), http.MethodGet, "/wanted/missing", Options{})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if res.Pagination == nil {
		t.Fatal("no pagination read from the body")
	}
	if res.Pagination.Page != 2 || res.Pagination.PageSize != 10 || res.Pagination.TotalRecords != 154 {
		t.Errorf("pagination = %+v", res.Pagination)
	}
}

// A bare array carries no pagination and must not invent any.
func TestNoPaginationOnBareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1},{"id":2}]`))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, "radarr")
	res, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if res.Pagination != nil {
		t.Errorf("pagination invented for a bare array: %+v", res.Pagination)
	}
}

// --all walks every page and merges the records into one paged body.
func TestAllPaginates(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			w.Write([]byte(`{"page":1,"pageSize":2,"totalRecords":3,"records":[{"id":1},{"id":2}]}`))
		default:
			w.Write([]byte(`{"page":2,"pageSize":2,"totalRecords":3,"records":[{"id":3}]}`))
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "sonarr")
	res, cerr := c.Do(context.Background(), http.MethodGet, "/wanted/missing",
		Options{All: true, Query: map[string][]string{"pageSize": {"2"}}})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if pages != 2 {
		t.Errorf("fetched %d pages, want 2", pages)
	}
	var body struct {
		Records      []json.RawMessage `json:"records"`
		TotalRecords int               `json:"totalRecords"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records) != 3 || body.TotalRecords != 3 {
		t.Errorf("merged %d of %d records", len(body.Records), body.TotalRecords)
	}
}

// --all stops at the page cap rather than hammering the instance forever.
func TestAllRunawayGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always claims there is more, never delivering the total.
		w.Write([]byte(`{"page":1,"pageSize":1,"totalRecords":100000,"records":[{"id":1}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "radarr")
	_, cerr := c.Do(context.Background(), http.MethodGet, "/history", Options{All: true})
	if cerr == nil {
		t.Fatal("--all ran away without erroring")
	}
	if cerr.Code != output.CodePaginationRunaway || cerr.Exit != output.ExitUser {
		t.Errorf("code = %s exit = %d, want PAGINATION_RUNAWAY / 1", cerr.Code, cerr.Exit)
	}
}

// A missing key fails before any request is made, with the storage hint.
func TestMissingKeyFailsClosed(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()

	c := New(Config{App: "radarr", BaseURL: srv.URL, Timeout: time.Second,
		KeyHint: "store the key in the Keychain"})
	_, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr == nil || cerr.Code != output.CodeBadConfig {
		t.Fatalf("cerr = %+v", cerr)
	}
	if hit {
		t.Error("a keyless request was still sent")
	}
}

// A connection failure is a TRANSPORT_* error at exit 3, and the message never
// carries the request URL — which under --apikey-in query holds the key.
func TestTransportErrorScrubsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	c := New(Config{App: "sonarr", BaseURL: url, APIKey: "secret-key-value",
		Timeout: 2 * time.Second, Placement: KeyInQuery})
	_, cerr := c.Do(context.Background(), http.MethodGet, "/queue", Options{})
	if cerr == nil {
		t.Fatal("a dead server returned success")
	}
	if cerr.Exit != output.ExitTransport {
		t.Errorf("exit = %d, want 3", cerr.Exit)
	}
	if !strings.HasPrefix(cerr.Code, "TRANSPORT_") {
		t.Errorf("code = %s", cerr.Code)
	}
	if strings.Contains(cerr.Message, "secret-key-value") || strings.Contains(cerr.Hint, "secret-key-value") {
		t.Fatalf("the API key leaked through a transport error: %s / %s", cerr.Message, cerr.Hint)
	}
}

// A request timeout maps to TRANSPORT_TIMEOUT, not a generic refusal.
func TestTimeoutMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	c := New(Config{App: "radarr", BaseURL: srv.URL, APIKey: "k", Timeout: 50 * time.Millisecond})
	_, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr == nil || cerr.Code != output.CodeTransportTimeout {
		t.Fatalf("cerr = %+v", cerr)
	}
}

// A POST body is sent as JSON with the right content type.
func TestPostBody(t *testing.T) {
	var got map[string]interface{}
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"id":536,"status":"queued"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "radarr")
	_, cerr := c.Do(context.Background(), http.MethodPost, "/command",
		Options{Body: map[string]interface{}{"name": "MoviesSearch", "movieIds": []int{37}}})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if got["name"] != "MoviesSearch" {
		t.Errorf("body = %+v", got)
	}
}

// A cross-origin redirect is refused, so a misconfigured instance cannot
// retarget the API key at another host.
func TestCrossOriginRedirectRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"stolen":true}`))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v3/movie", http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "radarr")
	_, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr == nil {
		t.Fatal("a cross-origin redirect was followed")
	}
}

// Not every Servarr paged body carries totalRecords. Terminating on
// len(merged) >= total treated an absent total as zero and stopped after the
// first page, so --all silently returned a fraction of the list and said it was
// the whole thing. Termination is the page itself: empty, or shorter than the
// page size the response reports.
func TestAllWithoutTotalRecords(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Query().Get("page") == "1" {
			w.Write([]byte(`{"page":1,"pageSize":2,"records":[{"id":1},{"id":2}]}`))
			return
		}
		w.Write([]byte(`{"page":2,"pageSize":2,"records":[{"id":3}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "radarr")
	res, cerr := c.Do(context.Background(), http.MethodGet, "/history",
		Options{All: true, Query: map[string][]string{"pageSize": {"2"}}})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if pages != 2 {
		t.Errorf("fetched %d pages, want 2", pages)
	}
	var body struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Records) != 3 {
		t.Errorf("merged %d records, want 3: --all dropped pages", len(body.Records))
	}
}

// --all from a later page reports the page it actually started at. Reporting a
// constant 1 told the caller it was holding the whole list from the top when it
// was holding a tail.
func TestAllFromLaterPage(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		got = append(got, p)
		if p == "3" {
			w.Write([]byte(`{"page":3,"pageSize":2,"totalRecords":8,"records":[{"id":5},{"id":6}]}`))
			return
		}
		w.Write([]byte(`{"page":4,"pageSize":2,"totalRecords":8,"records":[{"id":7},{"id":8}]}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv, "sonarr")
	res, cerr := c.Do(context.Background(), http.MethodGet, "/wanted/missing",
		Options{All: true, Query: map[string][]string{"page": {"3"}, "pageSize": {"2"}}})
	if cerr != nil {
		t.Fatal(cerr)
	}
	if strings.Join(got, ",") != "3,4" {
		t.Errorf("requested pages %v, want [3 4]", got)
	}
	var body struct {
		Page    int               `json:"page"`
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		t.Fatal(err)
	}
	if body.Page != 3 {
		t.Errorf("merged body page = %d, want 3", body.Page)
	}
	if res.Pagination == nil || res.Pagination.Page != 3 {
		t.Errorf("meta pagination = %+v, want page 3", res.Pagination)
	}
	if len(body.Records) != 4 {
		t.Errorf("merged %d records, want 4", len(body.Records))
	}
}

// A Servarr instance behind a reverse proxy has a path in its base URL. The
// request builder must keep it: dropping it sends every call to the proxy root.
func TestBuildRequestPreservesURLBase(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("pageSize")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	for _, base := range []string{srv.URL + "/radarr", srv.URL + "/radarr/"} {
		c := New(Config{App: "radarr", BaseURL: base, APIKey: "k", Timeout: 5 * time.Second})
		if _, cerr := c.Do(context.Background(), http.MethodGet, "/movie",
			Options{Query: map[string][]string{"pageSize": {"5"}}}); cerr != nil {
			t.Fatalf("%s: %v", base, cerr)
		}
		if gotPath != "/radarr/api/v3/movie" {
			t.Errorf("base %q produced path %q, want /radarr/api/v3/movie", base, gotPath)
		}
		if gotQuery != "5" {
			t.Errorf("base %q lost the query: pageSize = %q", base, gotQuery)
		}
	}
}
