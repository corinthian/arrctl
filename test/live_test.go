//go:build live

// Package live is the live smoke suite. It exercises the compiled binary
// against whatever Radarr and Sonarr instances the ordinary configuration
// resolves to — flags > env > config.toml > Keychain, the same path the
// production CLI uses. Nothing is hardcoded: no host, no key, no library id.
//
//	go test -tags=live ./test/...
//
// The whole suite skips when an instance is not configured or not reachable, so
// it is a no-op on a machine without access. Excluded from the default
// `go test` by the `live` build tag.
//
// SAFETY. Every call in this file is a read. There is no grab, no queue
// mutation, no delete, no add, no monitoring change, no restart, and no
// asynchronous command dispatch of any kind — not even a search, because a
// search on a monitored record starts downloads. The two confirm-gate cases
// prove refusal WITHOUT --confirm and therefore never reach the instance.
package live

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot resolves the repo root from this test file's location.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller")
	}
	return filepath.Dir(filepath.Dir(file)) // test/ -> repo root
}

// binary builds arrctl into a temp dir and returns its path, so the suite tests
// the compiled binary rather than the package internals.
func binary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "arrctl")
	cmd := exec.Command("go", "build", "-o", out, "./cmd/arrctl")
	cmd.Dir = repoRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building arrctl: %v\n%s", err, b)
	}
	return out
}

// envelope is the parsed {ok,data,meta} response.
type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		HTTPStatus int    `json:"http_status"`
		Hint       string `json:"hint"`
	} `json:"error"`
	Meta *struct {
		App        string `json:"app"`
		Endpoint   string `json:"endpoint"`
		DurationMS int64  `json:"duration_ms"`
		Pagination *struct {
			Page         int `json:"page"`
			PageSize     int `json:"page_size"`
			TotalRecords int `json:"total_records"`
		} `json:"pagination"`
	} `json:"meta"`
}

// run invokes the binary and decodes the envelope. It returns the exit code so
// a test can assert the error model, not just the body.
func run(t *testing.T, bin string, args ...string) (envelope, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = repoRoot(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	exit := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errorsAs(err, &ee) {
			t.Fatalf("running %v: %v", args, err)
		}
		exit = ee.ExitCode()
	}
	if s := stderr.String(); s != "" {
		t.Errorf("stderr is meant to stay unused, got: %s", s)
	}
	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("output of %v is not the envelope: %v\n%s", args, err, stdout.String())
	}
	return env, exit
}

func errorsAs(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

// reachable reports whether an app answers, skipping the suite when it does
// not. A configuration or transport failure is a skip, not a failure: this file
// must be a no-op on a machine with no access to the instances.
func reachable(t *testing.T, bin, app string) {
	t.Helper()
	env, exit := run(t, bin, app, "system", "status")
	if env.OK {
		return
	}
	code := ""
	if env.Error != nil {
		code = env.Error.Code
	}
	switch code {
	case "BAD_CONFIG", "TRANSPORT_DNS", "TRANSPORT_TLS", "TRANSPORT_TIMEOUT", "TRANSPORT_REFUSED":
		t.Skipf("%s not configured or not reachable (%s); skipping the live suite", app, code)
	}
	t.Fatalf("%s system status failed: %s (exit %d)", app, code, exit)
}

// apps returns the app names the suite exercises.
func apps() []string { return []string{"radarr", "sonarr"} }

// TestLiveReadsPerGroup does one read per shared group on both apps and asserts
// shape only — ok:true, meta.app correct, a body of the right kind. It never
// asserts a volatile count, title or id, so library drift cannot fail it.
func TestLiveReadsPerGroup(t *testing.T) {
	bin := binary(t)
	shared := []struct {
		name string
		args []string
		kind string // "array", "object", "paged"
	}{
		{"system-status", []string{"system", "status"}, "object"},
		{"system-task-list", []string{"system", "task", "list"}, "array"},
		{"health", []string{"health"}, "array"},
		{"diskspace", []string{"diskspace"}, "array"},
		{"ping", []string{"ping"}, "object"},
		{"api-info", []string{"api-info"}, "object"},
		{"rootfolder-list", []string{"rootfolder", "list"}, "array"},
		{"qualityprofile-list", []string{"qualityprofile", "list"}, "array"},
		{"qualityprofile-schema", []string{"qualityprofile", "schema"}, "object"},
		{"tag-list", []string{"tag", "list"}, "array"},
		{"tag-detail", []string{"tag", "detail"}, "array"},
		{"command-list", []string{"command", "list"}, "array"},
		{"queue-list", []string{"queue", "list", "--page-size", "1"}, "paged"},
		{"queue-status", []string{"queue", "status"}, "object"},
		{"history-list", []string{"history", "list", "--page-size", "1"}, "paged"},
		{"wanted-missing", []string{"wanted", "missing", "--page-size", "1"}, "paged"},
		{"wanted-cutoff", []string{"wanted", "cutoff", "--page-size", "1"}, "paged"},
		{"calendar-list", []string{"calendar", "list"}, "array"},
		{"filesystem", []string{"filesystem"}, "object"},
	}
	for _, app := range apps() {
		app := app
		t.Run(app, func(t *testing.T) {
			reachable(t, bin, app)
			for _, tc := range shared {
				t.Run(tc.name, func(t *testing.T) {
					env, exit := run(t, bin, append([]string{app}, tc.args...)...)
					if !env.OK || exit != 0 {
						t.Fatalf("exit %d, error %+v", exit, env.Error)
					}
					if env.Meta == nil || env.Meta.App != app {
						t.Errorf("meta.app = %+v, want %s", env.Meta, app)
					}
					assertKind(t, tc.kind, env)
				})
			}
		})
	}
}

func assertKind(t *testing.T, kind string, env envelope) {
	t.Helper()
	switch kind {
	case "array":
		var arr []json.RawMessage
		if err := json.Unmarshal(env.Data, &arr); err != nil {
			t.Errorf("data is not an array: %s", truncate(env.Data))
		}
	case "paged":
		var p struct {
			Records []json.RawMessage `json:"records"`
		}
		if err := json.Unmarshal(env.Data, &p); err != nil || p.Records == nil {
			t.Errorf("data is not a paged body: %s", truncate(env.Data))
		}
		if env.Meta.Pagination == nil {
			t.Error("a paged read carried no meta.pagination")
		}
	default:
		var o map[string]json.RawMessage
		if err := json.Unmarshal(env.Data, &o); err != nil {
			t.Errorf("data is not an object: %s", truncate(env.Data))
		}
	}
}

func truncate(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// TestLiveAppGroups reads the app-only groups. Ids are discovered at runtime
// from the library, never hardcoded.
func TestLiveAppGroups(t *testing.T) {
	bin := binary(t)

	t.Run("radarr", func(t *testing.T) {
		reachable(t, bin, "radarr")
		env, _ := run(t, bin, "radarr", "movie", "list")
		if !env.OK {
			t.Fatalf("movie list: %+v", env.Error)
		}
		var movies []struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(env.Data, &movies); err != nil {
			t.Fatalf("movie list is not an array: %v", err)
		}
		if len(movies) == 0 {
			t.Skip("empty Radarr library; nothing to read")
		}
		id := itoa(movies[0].ID)

		for _, tc := range []struct {
			name string
			args []string
		}{
			{"movie-get", []string{"radarr", "movie", "get", "--id", id}},
			{"movie-folder", []string{"radarr", "movie", "folder", "--id", id}},
			{"moviefile-list", []string{"radarr", "moviefile", "list", "--movie-id", id}},
			{"collection-list", []string{"radarr", "collection", "list"}},
			{"history-item", []string{"radarr", "history", "item", "--id", id}},
			{"rename-preview", []string{"radarr", "rename", "preview", "--movie-id", id}},
			{"parse", []string{"radarr", "parse", "--title", "Some.Film.1999.1080p.BluRay.x264"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env, exit := run(t, bin, tc.args...)
				if !env.OK || exit != 0 {
					t.Fatalf("exit %d, error %+v", exit, env.Error)
				}
			})
		}
	})

	t.Run("sonarr", func(t *testing.T) {
		reachable(t, bin, "sonarr")
		env, _ := run(t, bin, "sonarr", "series", "list")
		if !env.OK {
			t.Fatalf("series list: %+v", env.Error)
		}
		var series []struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(env.Data, &series); err != nil {
			t.Fatalf("series list is not an array: %v", err)
		}
		if len(series) == 0 {
			t.Skip("empty Sonarr library; nothing to read")
		}
		id := itoa(series[0].ID)

		for _, tc := range []struct {
			name string
			args []string
		}{
			{"series-get", []string{"sonarr", "series", "get", "--id", id}},
			{"series-folder", []string{"sonarr", "series", "folder", "--id", id}},
			{"episode-list", []string{"sonarr", "episode", "list", "--series-id", id}},
			{"episodefile-list", []string{"sonarr", "episodefile", "list", "--series-id", id}},
			{"history-item", []string{"sonarr", "history", "item", "--id", id}},
			{"rename-preview", []string{"sonarr", "rename", "preview", "--series-id", id}},
			{"parse", []string{"sonarr", "parse", "--title", "Some.Show.S01E01.720p"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				env, exit := run(t, bin, tc.args...)
				if !env.OK || exit != 0 {
					t.Fatalf("exit %d, error %+v", exit, env.Error)
				}
			})
		}
	})
}

// TestLiveAuthFailure proves the auth error path against a real instance using
// a deliberately wrong key supplied through the environment. The Keychain is
// never touched.
func TestLiveAuthFailure(t *testing.T) {
	bin := binary(t)
	for _, app := range apps() {
		app := app
		t.Run(app, func(t *testing.T) {
			reachable(t, bin, app)
			cmd := exec.Command(bin, app, "queue", "list")
			cmd.Dir = repoRoot(t)
			cmd.Env = append(os.Environ(),
				"ARRCTL_"+strings.ToUpper(app)+"_APIKEY=deliberately-wrong-not-a-real-key")
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			exit := 0
			if err := cmd.Run(); err != nil {
				var ee *exec.ExitError
				if !errorsAs(err, &ee) {
					t.Fatal(err)
				}
				exit = ee.ExitCode()
			}
			var env envelope
			if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
				t.Fatalf("not the envelope: %s", stdout.String())
			}
			if env.OK {
				t.Fatal("a wrong key was accepted")
			}
			if env.Error.Code != "ARR_AUTH_FAILED" {
				t.Errorf("code = %s, want ARR_AUTH_FAILED", env.Error.Code)
			}
			if exit != 2 {
				t.Errorf("exit = %d, want 2", exit)
			}
			if !strings.Contains(env.Error.Hint, "add-generic-password") ||
				!strings.Contains(env.Error.Hint, app+"ctl") {
				t.Errorf("hint does not name the Keychain recovery line: %q", env.Error.Hint)
			}
			// The key value must never appear in the output.
			if strings.Contains(stdout.String(), "deliberately-wrong-not-a-real-key") {
				t.Error("the API key was echoed in the output")
			}
		})
	}
}

// TestLiveConfirmGateRefuses proves a gated mutation refuses BEFORE any request
// is made. Nothing reaches the instance, which is why running this against a
// live library is safe.
func TestLiveConfirmGateRefuses(t *testing.T) {
	bin := binary(t)
	reachable(t, bin, "radarr")

	env, _ := run(t, bin, "radarr", "movie", "list")
	var movies []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(env.Data, &movies); err != nil || len(movies) == 0 {
		t.Skip("empty Radarr library")
	}
	id := itoa(movies[0].ID)

	got, exit := run(t, bin, "radarr", "movie", "delete", "--id", id)
	if got.OK {
		t.Fatal("an ungated delete was accepted")
	}
	if got.Error.Code != "CONFIRM_REQUIRED" || exit != 1 {
		t.Fatalf("code = %s, exit = %d; want CONFIRM_REQUIRED / 1", got.Error.Code, exit)
	}

	// And the record is still there.
	after, exit := run(t, bin, "radarr", "movie", "get", "--id", id)
	if !after.OK || exit != 0 {
		t.Fatalf("the film is gone after a refused delete: %+v", after.Error)
	}
}

// TestLiveMissingAppIsUsageError: the app is required, with no default.
func TestLiveMissingApp(t *testing.T) {
	bin := binary(t)
	env, exit := run(t, bin, "queue", "list")
	if env.OK {
		t.Fatal("`arrctl queue list` with no app succeeded")
	}
	if env.Error.Code != "BAD_REQUEST" || exit != 1 {
		t.Errorf("code = %s, exit = %d; want BAD_REQUEST / 1", env.Error.Code, exit)
	}
}

// TestLiveCommandsTree: `arrctl commands` tags every group with its apps.
func TestLiveCommandsTree(t *testing.T) {
	bin := binary(t)
	env, exit := run(t, bin, "commands")
	if !env.OK || exit != 0 {
		t.Fatalf("exit %d: %+v", exit, env.Error)
	}
	var data struct {
		Groups []struct {
			Name string   `json:"name"`
			Apps []string `json:"apps"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	for _, g := range data.Groups {
		if g.Name == "queue" {
			if strings.Join(g.Apps, ",") != "radarr,sonarr" {
				t.Errorf("queue apps = %v, want [radarr sonarr]", g.Apps)
			}
			return
		}
	}
	t.Error("no queue group in the command tree")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
