package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

// run executes the root command with args, capturing stdout and returning the
// typed error (nil on success). No network call is made by any test here: every
// case fails before the client is used, or targets a local-only command.
func run(t *testing.T, args ...string) (string, *output.CLIError) {
	t.Helper()
	root, app := NewRoot()
	var buf bytes.Buffer
	app.Out.Out = &buf
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return buf.String(), nil
	}
	return buf.String(), classifyError(err)
}

// findCmd walks the tree to a command path like "radarr queue list".
func findCmd(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	root, _ := NewRoot()
	cur := root
	for _, name := range path {
		next := findChild(cur, name)
		if next == nil {
			t.Fatalf("command %q not found under %q", name, cur.CommandPath())
		}
		cur = next
	}
	return cur
}

// The app is required and has no default: `arrctl queue list` with no app must
// be a usage error at exit 1, never a call against whichever instance happened
// to be configured.
func TestMissingAppIsBadRequest(t *testing.T) {
	_, cerr := run(t, "queue", "list")
	if cerr == nil {
		t.Fatal("`arrctl queue list` succeeded; the app must be required")
	}
	if cerr.Code != output.CodeBadRequest {
		t.Errorf("code = %s, want BAD_REQUEST", cerr.Code)
	}
	if got := cerr.Exit; got != output.ExitUser {
		t.Errorf("exit = %d, want 1", got)
	}
	if !strings.Contains(cerr.Hint, "radarr") || !strings.Contains(cerr.Hint, "sonarr") {
		t.Errorf("hint does not name the two apps: %q", cerr.Hint)
	}
}

func TestUnknownAppIsBadRequest(t *testing.T) {
	_, cerr := run(t, "lidarr", "queue", "list")
	if cerr == nil || cerr.Code != output.CodeBadRequest {
		t.Fatalf("unknown app not rejected: %+v", cerr)
	}
}

// A group with no verb reports a missing subcommand at exit 1 rather than
// printing help at exit 0.
func TestGroupWithoutVerbIsBadRequest(t *testing.T) {
	_, cerr := run(t, "radarr", "queue")
	if cerr == nil || cerr.Code != output.CodeBadRequest {
		t.Fatalf("bare group not rejected: %+v", cerr)
	}
	if !strings.Contains(cerr.Message, "missing subcommand") {
		t.Errorf("message = %q", cerr.Message)
	}
}

// The flag table is the whole of the difference between the two apps. Every
// app-specific flag must bind to the query key the OpenAPI document names.
func TestFlagTableQueryKeys(t *testing.T) {
	cases := []struct {
		path       []string
		flag       string
		wantQuery  string
		wantAbsent bool
	}{
		// queue list
		{[]string{"radarr", "queue", "list"}, "movie-ids", "movieIds", false},
		{[]string{"radarr", "queue", "list"}, "include-movie", "includeMovie", false},
		{[]string{"radarr", "queue", "list"}, "include-unknown-movie-items", "includeUnknownMovieItems", false},
		{[]string{"sonarr", "queue", "list"}, "series-ids", "seriesIds", false},
		{[]string{"sonarr", "queue", "list"}, "include-episode", "includeEpisode", false},
		{[]string{"sonarr", "queue", "list"}, "include-unknown-series-items", "includeUnknownSeriesItems", false},
		// queue details
		{[]string{"radarr", "queue", "details"}, "movie-id", "movieId", false},
		{[]string{"sonarr", "queue", "details"}, "episode-ids", "episodeIds", false},
		// history
		{[]string{"radarr", "history", "list"}, "movie-ids", "movieIds", false},
		{[]string{"sonarr", "history", "list"}, "episode-id", "episodeId", false},
		// wanted
		{[]string{"radarr", "wanted", "missing"}, "monitored", "monitored", false},
		{[]string{"sonarr", "wanted", "cutoff"}, "include-episode-file", "includeEpisodeFile", false},
		// release
		{[]string{"radarr", "release", "search"}, "movie-id", "movieId", false},
		{[]string{"sonarr", "release", "search"}, "season", "seasonNumber", false},
		// calendar
		{[]string{"sonarr", "calendar", "list"}, "include-episode-images", "includeEpisodeImages", false},
		// rename / manualimport / parse
		{[]string{"radarr", "rename", "preview"}, "movie-id", "movieId", false},
		{[]string{"sonarr", "manualimport", "list"}, "season", "seasonNumber", false},
		{[]string{"sonarr", "parse"}, "path", "path", false},
		// resource lists
		{[]string{"radarr", "movie", "list"}, "tmdb-id", "tmdbId", false},
		{[]string{"sonarr", "series", "list"}, "tvdb-id", "tvdbId", false},
		// delete exclusion differs by app
		{[]string{"radarr", "movie", "delete"}, "add-exclusion", "addImportExclusion", false},
		{[]string{"sonarr", "series", "delete"}, "add-exclusion", "addImportListExclusion", false},
	}
	for _, tc := range cases {
		name := strings.Join(tc.path, "/") + ":--" + tc.flag
		t.Run(name, func(t *testing.T) {
			q := lookupQueryKey(t, tc.path, tc.flag)
			if q != tc.wantQuery {
				t.Errorf("--%s maps to %q, want %q", tc.flag, q, tc.wantQuery)
			}
		})
	}
}

// Flags belonging to the other app must not exist: `radarr queue list
// --series-ids` has to fail, not be silently ignored.
func TestCrossAppFlagsAbsent(t *testing.T) {
	absent := []struct {
		path []string
		flag string
	}{
		{[]string{"radarr", "queue", "list"}, "series-ids"},
		{[]string{"radarr", "queue", "list"}, "include-series"},
		{[]string{"sonarr", "queue", "list"}, "movie-ids"},
		{[]string{"sonarr", "queue", "list"}, "include-movie"},
		{[]string{"radarr", "parse"}, "path"},
		{[]string{"radarr", "wanted", "missing"}, "include-series"},
	}
	for _, tc := range absent {
		c := findCmd(t, tc.path...)
		if c.Flags().Lookup(tc.flag) != nil {
			t.Errorf("%s should not define --%s", c.CommandPath(), tc.flag)
		}
	}
}

// lookupQueryKey finds the qflag a command binds for a CLI flag name, by
// consulting the spec tables the command was built from.
func lookupQueryKey(t *testing.T, path []string, flag string) string {
	t.Helper()
	c := findCmd(t, path...)
	if c.Flags().Lookup(flag) == nil {
		t.Fatalf("%s does not define --%s", c.CommandPath(), flag)
	}
	spec := specs[config.App(path[0])]
	for _, set := range [][]qflag{
		spec.QueueList, spec.QueueDetails, spec.HistoryList, spec.HistorySince,
		spec.WantedMissing, spec.WantedCutoff, spec.ReleaseSearch, spec.CalendarList,
		spec.RenamePreview, spec.ManualImport, spec.Parse, spec.HistoryItemExtra,
		spec.ResourceListFlags, deleteFlagsFor(spec), filterFlags, calendarShared,
		manualImportShared, queueDeleteFlags,
	} {
		for _, f := range set {
			if f.Name == flag {
				return f.Query
			}
		}
	}
	t.Fatalf("no qflag row for --%s on %s", flag, path[0])
	return ""
}

// The history item path and query key differ per app.
func TestHistoryItemPathPerApp(t *testing.T) {
	if radarrSpec.HistoryItemPath != "/history/movie" || radarrSpec.HistoryItemQuery != "movieId" {
		t.Errorf("radarr history item = %s?%s", radarrSpec.HistoryItemPath, radarrSpec.HistoryItemQuery)
	}
	if sonarrSpec.HistoryItemPath != "/history/series" || sonarrSpec.HistoryItemQuery != "seriesId" {
		t.Errorf("sonarr history item = %s?%s", sonarrSpec.HistoryItemPath, sonarrSpec.HistoryItemQuery)
	}
}

// --id-type is per-app and rejects anything outside each app's set. An
// orchestrator must resolve a Trakt id before calling arrctl.
func TestIDTypeRejectedPerApp(t *testing.T) {
	if cerr := radarrSpec.validIDType("trakt"); cerr == nil {
		t.Error("radarr accepted --id-type trakt")
	} else if cerr.Code != output.CodeBadRequest {
		t.Errorf("code = %s, want BAD_REQUEST", cerr.Code)
	}
	if cerr := sonarrSpec.validIDType("trakt"); cerr == nil {
		t.Error("sonarr accepted --id-type trakt")
	}
	// Radarr has no tvdb id space; Sonarr does.
	if cerr := radarrSpec.validIDType("tvdb"); cerr == nil {
		t.Error("radarr accepted --id-type tvdb")
	}
	if cerr := sonarrSpec.validIDType("tvdb"); cerr != nil {
		t.Errorf("sonarr rejected --id-type tvdb: %v", cerr)
	}
	for _, tt := range radarrSpec.IDTypes {
		if cerr := radarrSpec.validIDType(tt); cerr != nil {
			t.Errorf("radarr rejected its own id type %s", tt)
		}
	}
}

// Radarr serves external ids on dedicated lookup paths; Sonarr folds them into
// the single lookup term.
func TestLookupByIDShape(t *testing.T) {
	p, q := radarrSpec.lookupByID("tmdb", "603")
	if p != "/movie/lookup/tmdb" || q["tmdbId"][0] != "603" {
		t.Errorf("radarr tmdb lookup = %s %v", p, q)
	}
	p, q = radarrSpec.lookupByID("imdb", "tt0067023")
	if p != "/movie/lookup/imdb" || q["imdbId"][0] != "tt0067023" {
		t.Errorf("radarr imdb lookup = %s %v", p, q)
	}
	p, q = sonarrSpec.lookupByID("tvdb", "12345")
	if p != "/series/lookup" || q["term"][0] != "tvdb:12345" {
		t.Errorf("sonarr tvdb lookup = %s %v", p, q)
	}
}

// Every action the spec's safety section gates must refuse without --confirm,
// and refuse with CONFIRM_REQUIRED at exit 1, having changed nothing. These run
// with a bogus URL so that a gate failure cannot reach a live instance: a
// regression here fails the test rather than mutating a library.
func TestConfirmGates(t *testing.T) {
	gated := [][]string{
		{"radarr", "movie", "delete", "--id", "37"},
		{"sonarr", "series", "delete", "--id", "11"},
		{"radarr", "movie", "editor-delete", "--payload", "{}"},
		{"radarr", "movie", "import", "--payload", "{}"},
		{"radarr", "movie", "add", "--payload", "{}"},
		{"sonarr", "series", "add", "--payload", "{}"},
		{"sonarr", "seasonpass", "--payload", "{}"},
		{"sonarr", "episode", "monitor", "--episode-ids", "1", "--monitored", "true"},
		{"radarr", "moviefile", "delete", "--id", "1"},
		{"sonarr", "episodefile", "bulk-delete", "--payload", "{}"},
		{"radarr", "queue", "delete", "--id", "1"},
		{"radarr", "queue", "delete-bulk", "--payload", "{}"},
		{"radarr", "queue", "grab", "--id", "1"},
		{"radarr", "release", "push", "--payload", "{}"},
		{"radarr", "manualimport", "submit", "--payload", "{}"},
		{"radarr", "system", "restart"},
		{"sonarr", "system", "shutdown"},
		{"radarr", "tag", "delete", "--id", "1"},
		{"radarr", "rootfolder", "delete", "--id", "1"},
		{"radarr", "qualityprofile", "delete", "--id", "1"},
		{"radarr", "rss", "sync"},
		{"sonarr", "wanted", "search"},
		{"radarr", "command", "run", "--name", "SomethingUnenumerated"},
	}
	for _, args := range gated {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			full := append([]string{}, args...)
			full = append(full, "--url", "http://127.0.0.1:1", "--apikey", "test-only")
			_, cerr := run(t, full...)
			if cerr == nil {
				t.Fatalf("%v was not gated", args)
			}
			if cerr.Code != output.CodeConfirmRequired {
				t.Fatalf("code = %s, want CONFIRM_REQUIRED (message: %s)", cerr.Code, cerr.Message)
			}
			if cerr.Exit != output.ExitUser {
				t.Errorf("exit = %d, want 1", cerr.Exit)
			}
		})
	}
}

// `release grab` is deliberately NOT gated: grabbing is the point of the tool,
// and the size-disclosure rule lives in the skill.
func TestReleaseGrabIsNotGated(t *testing.T) {
	c := findCmd(t, "radarr", "release", "grab")
	if c == nil {
		t.Fatal("release grab missing")
	}
	// It must fail on the missing payload, not on a confirm gate.
	_, cerr := run(t, "radarr", "release", "grab", "--url", "http://127.0.0.1:1", "--apikey", "x")
	if cerr == nil || cerr.Code == output.CodeConfirmRequired {
		t.Fatalf("release grab is confirm-gated: %+v", cerr)
	}
}

// Reads never gate.
func TestReadsAreNotGated(t *testing.T) {
	for _, args := range [][]string{
		{"radarr", "movie", "list"},
		{"sonarr", "series", "list"},
		{"radarr", "queue", "list"},
	} {
		full := append(append([]string{}, args...), "--url", "http://127.0.0.1:1", "--apikey", "x")
		_, cerr := run(t, full...)
		if cerr != nil && cerr.Code == output.CodeConfirmRequired {
			t.Errorf("%v is gated but is a read", args)
		}
	}
}

// The `command run` allowlist: the sugar names run without --confirm, anything
// else does not, because the command-name set is not enumerated in the API doc.
func TestCommandRunAllowlist(t *testing.T) {
	for _, name := range []string{"MoviesSearch", "RefreshMovie", "RescanMovie"} {
		if !runAllowlist(radarrSpec)[name] {
			t.Errorf("radarr allowlist missing %s", name)
		}
	}
	for _, name := range []string{"SeriesSearch", "SeasonSearch", "EpisodeSearch",
		"RefreshSeries", "RescanSeries"} {
		if !runAllowlist(sonarrSpec)[name] {
			t.Errorf("sonarr allowlist missing %s", name)
		}
	}
	if runAllowlist(radarrSpec)["Backup"] || runAllowlist(radarrSpec)["DeleteMovies"] {
		t.Error("allowlist is not closed")
	}
}

// The library-wide command names must NOT be on the allowlist. Their sugar
// verbs are confirm-gated because they grab whatever they find; the raw
// `command run` path to the same body has to be gated too, or the gate is
// decorative.
func TestLibraryWideCommandsAreGated(t *testing.T) {
	for _, name := range []string{"MissingMoviesSearch", "RssSync"} {
		if runAllowlist(radarrSpec)[name] {
			t.Errorf("radarr allows %s without --confirm", name)
		}
	}
	for _, name := range []string{"MissingEpisodeSearch", "RssSync"} {
		if runAllowlist(sonarrSpec)[name] {
			t.Errorf("sonarr allows %s without --confirm", name)
		}
	}
	for _, args := range [][]string{
		{"radarr", "command", "run", "--name", "MissingMoviesSearch"},
		{"sonarr", "command", "run", "--name", "MissingEpisodeSearch"},
		{"radarr", "command", "run", "--name", "RssSync"},
		{"sonarr", "command", "run", "--name", "RssSync"},
	} {
		full := append(append([]string{}, args...), "--url", "http://127.0.0.1:1", "--apikey", "x")
		_, cerr := run(t, full...)
		if cerr == nil || cerr.Code != output.CodeConfirmRequired {
			t.Errorf("%v was not gated: %+v", args, cerr)
		}
	}
}

// The sugar's bodies are the exact `command run` equivalents from the spec.
func TestSugarBodies(t *testing.T) {
	cases := []struct {
		name string
		ids  []int
		want string
	}{
		{"MoviesSearch", []int{37}, `{"movieIds":[37],"name":"MoviesSearch"}`},
		{"RefreshMovie", []int{37}, `{"movieIds":[37],"name":"RefreshMovie"}`},
		{"RescanMovie", []int{37}, `{"movieId":37,"name":"RescanMovie"}`},
		{"SeriesSearch", []int{11}, `{"name":"SeriesSearch","seriesId":11}`},
		{"RefreshSeries", []int{11}, `{"name":"RefreshSeries","seriesId":11}`},
		{"RescanSeries", []int{11}, `{"name":"RescanSeries","seriesId":11}`},
	}
	for _, tc := range cases {
		body, cerr := commandBodyFor(tc.name, tc.ids)
		if cerr != nil {
			t.Fatalf("%s rejected %v: %+v", tc.name, tc.ids, cerr)
		}
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("%s body = %s, want %s", tc.name, b, tc.want)
		}
	}
}

// ARRCTL_CONFIRM=1 is the documented environment equivalent of --confirm.
func TestConfirmViaEnvironment(t *testing.T) {
	os.Setenv("ARRCTL_CONFIRM", "1")
	defer os.Unsetenv("ARRCTL_CONFIRM")
	_, cerr := run(t, "radarr", "movie", "delete", "--id", "37",
		"--url", "http://127.0.0.1:1", "--apikey", "x")
	if cerr != nil && cerr.Code == output.CodeConfirmRequired {
		t.Error("ARRCTL_CONFIRM=1 did not satisfy the gate")
	}
}

// `arrctl commands` emits one entry per group with the apps it exists on, not
// the raw cobra tree (which would list every shared group twice).
func TestCommandsTreeGroupsAndApps(t *testing.T) {
	out, cerr := run(t, "commands")
	if cerr != nil {
		t.Fatal(cerr)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Groups []struct {
				Name string   `json:"name"`
				Apps []string `json:"apps"`
			} `json:"groups"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("commands output is not the envelope: %v\n%s", err, out)
	}
	if !env.OK {
		t.Error("ok = false")
	}
	got := map[string][]string{}
	for _, g := range env.Data.Groups {
		if _, dup := got[g.Name]; dup {
			t.Errorf("group %q listed twice", g.Name)
		}
		got[g.Name] = g.Apps
	}
	want := map[string][]string{
		"queue":      {"radarr", "sonarr"},
		"history":    {"radarr", "sonarr"},
		"movie":      {"radarr"},
		"collection": {"radarr"},
		"series":     {"sonarr"},
		"episode":    {"sonarr"},
		"seasonpass": {"sonarr"},
	}
	for name, apps := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("group %q missing from the tree", name)
			continue
		}
		if strings.Join(g, ",") != strings.Join(apps, ",") {
			t.Errorf("group %q apps = %v, want %v", name, g, apps)
		}
	}
}

// --llm emits JSON help, not human help, and never exits non-zero.
func TestLLMHelpIsJSON(t *testing.T) {
	root, app := NewRoot()
	var buf bytes.Buffer
	app.Out.Out = &buf
	c := findChild(findChild(root, "radarr"), "queue")
	emitLLMHelp(findChild(c, "list"), app.Out)
	var h llmHelp
	if err := json.Unmarshal(buf.Bytes(), &h); err != nil {
		t.Fatalf("--llm output is not JSON: %v\n%s", err, buf.String())
	}
	if h.App != "radarr" {
		t.Errorf("app = %q, want radarr", h.App)
	}
	if h.OutputSchema == "" || len(h.Examples) == 0 {
		t.Error("--llm help has no output_schema or examples")
	}
}

// An invalid --sort-dir is caught locally rather than sent to the instance.
func TestSortDirValidated(t *testing.T) {
	_, cerr := run(t, "radarr", "queue", "list", "--sort-dir", "sideways",
		"--url", "http://127.0.0.1:1", "--apikey", "x")
	if cerr == nil || cerr.Code != output.CodeBadRequest {
		t.Fatalf("bad --sort-dir not rejected: %+v", cerr)
	}
}

func TestAPIKeyInValidated(t *testing.T) {
	_, cerr := run(t, "radarr", "system", "status", "--apikey-in", "cookie",
		"--url", "http://127.0.0.1:1", "--apikey", "x")
	if cerr == nil || cerr.Code != output.CodeBadRequest {
		t.Fatalf("bad --apikey-in not rejected: %+v", cerr)
	}
}

// Every group registered for an app must actually appear under that sub-root,
// and no Radarr-only group may appear under sonarr (or vice versa).
func TestGroupRegistrationPerApp(t *testing.T) {
	root, _ := NewRoot()
	radarrOnly := []string{"movie", "moviefile", "collection"}
	sonarrOnly := []string{"series", "episode", "episodefile", "seasonpass", "season"}
	for _, name := range radarrOnly {
		if findChild(findChild(root, "radarr"), name) == nil {
			t.Errorf("radarr missing group %s", name)
		}
		if findChild(findChild(root, "sonarr"), name) != nil {
			t.Errorf("sonarr wrongly has group %s", name)
		}
	}
	for _, name := range sonarrOnly {
		if findChild(findChild(root, "sonarr"), name) == nil {
			t.Errorf("sonarr missing group %s", name)
		}
		if findChild(findChild(root, "radarr"), name) != nil {
			t.Errorf("radarr wrongly has group %s", name)
		}
	}
}

// A command that reaches a terminal FAILED state is a failure, not a success:
// `--wait` exists to report the outcome, and reporting exit 0 for a search that
// failed is the worst possible answer for an unattended caller. The record is
// preserved in the message so the caller need not re-read it.
func TestWaitOnFailedCommandIsArrError(t *testing.T) {
	for _, status := range []string{"failed", "aborted", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id":1,"name":"SeriesSearch","status":%q,"message":"indexer unavailable"}`, status)
			}))
			defer srv.Close()

			_, cerr := run(t, "sonarr", "command", "wait", "--id", "1",
				"--url", srv.URL, "--apikey", "test-only")
			if cerr == nil {
				t.Fatalf("a %s command reported success", status)
			}
			if cerr.Code != output.CodeArrError {
				t.Errorf("code = %s, want ARR_ERROR", cerr.Code)
			}
			if cerr.Exit != output.ExitArr {
				t.Errorf("exit = %d, want 2", cerr.Exit)
			}
			if cerr.HTTPStatus != 0 {
				t.Errorf("http_status = %d, want it omitted: the HTTP call returned 200", cerr.HTTPStatus)
			}
			for _, want := range []string{"SeriesSearch", "#1", status, "indexer unavailable"} {
				if !strings.Contains(cerr.Message, want) {
					t.Errorf("message %q does not carry %q", cerr.Message, want)
				}
			}
			if !strings.Contains(cerr.Hint, "command get --id 1") {
				t.Errorf("hint %q does not point at the record", cerr.Hint)
			}
		})
	}
}

// waitAndEmit is a separate wrapper around pollCommand, so the sugar's --wait
// needs its own case: the fix has to reach both paths.
func TestSugarWaitOnFailedCommandIsArrError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"id":7,"name":"SeriesSearch","status":"queued"}`)
			return
		}
		fmt.Fprint(w, `{"id":7,"name":"SeriesSearch","status":"failed","message":"no results"}`)
	}))
	defer srv.Close()

	_, cerr := run(t, "sonarr", "series", "search", "--id", "11", "--wait",
		"--url", srv.URL, "--apikey", "test-only")
	if cerr == nil {
		t.Fatal("a failed search reported success through the sugar's --wait")
	}
	if cerr.Code != output.CodeArrError || cerr.Exit != output.ExitArr {
		t.Errorf("code = %s exit = %d, want ARR_ERROR / 2", cerr.Code, cerr.Exit)
	}
}

// `completed` is unchanged: it is the one terminal status that means success.
func TestWaitOnCompletedCommandStaysSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1,"name":"SeriesSearch","status":"completed","message":"1 report downloaded"}`)
	}))
	defer srv.Close()

	out, cerr := run(t, "sonarr", "command", "wait", "--id", "1",
		"--url", srv.URL, "--apikey", "test-only")
	if cerr != nil {
		t.Fatalf("a completed command was reported as a failure: %+v", cerr)
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("output does not carry the record: %s", out)
	}
}

// The singular command shapes carry ONE id. Handing them several used to build
// a body from ids[0] and drop the rest silently: the caller was told a search
// had been dispatched for every id when only the first had been. An error is
// the honest answer — the envelope is single-record shaped, so fanning out
// would need a multi-result contract this tool does not have.
func TestSingularCommandRejectsMultipleIDs(t *testing.T) {
	for _, name := range []string{"RescanMovie", "SeriesSearch", "RefreshSeries", "RescanSeries"} {
		t.Run(name, func(t *testing.T) {
			body, cerr := commandBodyFor(name, []int{11, 12})
			if cerr == nil {
				b, _ := json.Marshal(body)
				t.Fatalf("%s accepted two ids and built %s", name, b)
			}
			if cerr.Code != output.CodeBadRequest || cerr.Exit != output.ExitUser {
				t.Errorf("code = %s exit = %d, want BAD_REQUEST / 1", cerr.Code, cerr.Exit)
			}
			if cerr.Hint == "" {
				t.Error("no hint naming the one-at-a-time loop")
			}
		})
	}
}

// The plural shapes are unaffected: `radarr movie search --id 1,2` is a
// documented, working invocation and must stay one.
func TestPluralCommandsStillTakeManyIDs(t *testing.T) {
	for _, name := range []string{"MoviesSearch", "RefreshMovie"} {
		body, cerr := commandBodyFor(name, []int{11, 12})
		if cerr != nil {
			t.Fatalf("%s rejected two ids: %+v", name, cerr)
		}
		b, _ := json.Marshal(body)
		if !strings.Contains(string(b), "12") {
			t.Errorf("%s dropped the second id: %s", name, b)
		}
	}
}

// A non-positive id is a usage error wherever it is typed, matching requireID.
// Servarr has no record 0 and no record -3; sending either is a wasted round
// trip at best and a confusing 404 at worst.
func TestIntListRejectsNonPositive(t *testing.T) {
	for _, raw := range []string{"0", "-3"} {
		if _, cerr := intList([]string{raw}, "--id"); cerr == nil {
			t.Errorf("intList accepted %q", raw)
		} else if cerr.Code != output.CodeBadRequest || cerr.Exit != output.ExitUser {
			t.Errorf("%q: code = %s exit = %d, want BAD_REQUEST / 1", raw, cerr.Code, cerr.Exit)
		}
	}
	if _, cerr := intList([]string{"7", "0"}, "--id"); cerr == nil {
		t.Error("intList accepted a non-positive id alongside a valid one")
	}
	if got, cerr := intList([]string{"7", "9"}, "--id"); cerr != nil || len(got) != 2 {
		t.Errorf("intList rejected valid ids: %v %+v", got, cerr)
	}
}

// End to end: the rejection happens before any POST is made.
func TestSugarRejectsMultipleIDsBeforeDispatch(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		fmt.Fprint(w, `{"id":1,"name":"SeriesSearch","status":"completed"}`)
	}))
	defer srv.Close()

	_, cerr := run(t, "sonarr", "series", "search", "--id", "11,12",
		"--url", srv.URL, "--apikey", "test-only")
	if cerr == nil || cerr.Code != output.CodeBadRequest {
		t.Fatalf("two ids were not rejected: %+v", cerr)
	}
	if posts != 0 {
		t.Errorf("%d commands were dispatched before the rejection", posts)
	}
}

// `config path` is the read-only diagnostic for a broken config path, so it
// must survive one. It reports the failure in its payload rather than dying.
func TestConfigPathToleratesBadExplicitPath(t *testing.T) {
	out, cerr := run(t, "config", "path", "--config", "/nonexistent/arrctl/x.toml")
	if cerr != nil {
		t.Fatalf("config path died on the path it exists to diagnose: %+v", cerr)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Found    bool   `json:"config_found"`
			Explicit bool   `json:"config_explicit"`
			Error    string `json:"config_error"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not the envelope: %v\n%s", err, out)
	}
	if !env.OK {
		t.Error("ok = false")
	}
	if env.Data.Found {
		t.Error("config_found = true for a path that does not exist")
	}
	if !env.Data.Explicit {
		t.Error("config_explicit = false for an explicit --config")
	}
	if env.Data.Error == "" {
		t.Error("config_error is empty; it must say why the path is unusable")
	}
}

// Tolerance is about the path, not the contents. A file `config path` can read
// and cannot parse is a hard error there too: the diagnostic exists to explain
// an unusable path, and reporting "no config" for a file that is plainly
// present would be the wrong answer.
func TestConfigPathDoesNotTolerateAnUnparseableFile(t *testing.T) {
	bad := writeUnparseableConfig(t)
	_, cerr := run(t, "config", "path", "--config", bad)
	if cerr == nil {
		t.Fatal("config path forgave an unparseable file")
	}
	if cerr.Code != output.CodeBadConfig || cerr.Exit != output.ExitUser {
		t.Errorf("code = %s exit = %d, want BAD_CONFIG / 1", cerr.Code, cerr.Exit)
	}
}

// `config init --force` is tolerant so it can name a file that does not exist
// yet. That must not become licence to overwrite a file it failed to
// understand: the config may hold settings the caller still wants.
func TestConfigInitOverUnparseableFileFailsBadConfig(t *testing.T) {
	bad := writeUnparseableConfig(t)
	before, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	_, cerr := run(t, "config", "init", "--force", "--config", bad,
		"--radarr-url", "http://127.0.0.1:7878")
	if cerr == nil {
		t.Fatal("config init overwrote a config file it could not parse")
	}
	if cerr.Code != output.CodeBadConfig || cerr.Exit != output.ExitUser {
		t.Errorf("code = %s exit = %d, want BAD_CONFIG / 1", cerr.Code, cerr.Exit)
	}
	after, err := os.ReadFile(bad)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the file was modified:\n%s", after)
	}
}

// writeUnparseableConfig writes a file that is present and readable and is not
// TOML, with the default config path pointed somewhere empty.
func writeUnparseableConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[radarr\nurl = "), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Every other command fails closed on it, at exit 1.
func TestBadExplicitConfigIsBadConfig(t *testing.T) {
	_, cerr := run(t, "radarr", "system", "status", "--config", "/nonexistent/arrctl/x.toml",
		"--url", "http://127.0.0.1:1", "--apikey", "x")
	if cerr == nil {
		t.Fatal("a missing --config path was silently ignored")
	}
	if cerr.Code != output.CodeBadConfig || cerr.Exit != output.ExitUser {
		t.Errorf("code = %s exit = %d, want BAD_CONFIG / 1", cerr.Code, cerr.Exit)
	}
}

// A wait configuration that can never poll must be caught BEFORE the command
// is dispatched. Validating after the POST leaves a command running on the
// instance with nothing watching it, and reports a usage error for work that
// has already started.
func TestWaitTimeoutValidatedBeforeDispatch(t *testing.T) {
	cases := [][]string{
		{"sonarr", "series", "search", "--id", "11", "--wait", "--wait-timeout", "0"},
		{"sonarr", "series", "search", "--id", "11", "--wait", "--wait-interval", "0"},
		{"radarr", "rss", "sync", "--confirm", "--wait", "--wait-timeout", "-1s"},
		{"radarr", "command", "run", "--name", "MoviesSearch", "--wait", "--wait-timeout", "0"},
		{"sonarr", "command", "wait", "--id", "1", "--timeout", "0"},
		{"sonarr", "command", "wait", "--id", "1", "--interval", "0"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				fmt.Fprint(w, `{"id":1,"name":"X","status":"completed"}`)
			}))
			defer srv.Close()

			full := append(append([]string{}, args...), "--url", srv.URL, "--apikey", "test-only")
			_, cerr := run(t, full...)
			if cerr == nil {
				t.Fatal("a non-positive wait duration was accepted")
			}
			if cerr.Code != output.CodeBadRequest || cerr.Exit != output.ExitUser {
				t.Errorf("code = %s exit = %d, want BAD_REQUEST / 1", cerr.Code, cerr.Exit)
			}
			if requests != 0 {
				t.Errorf("%d requests were made before the rejection", requests)
			}
		})
	}
}

// Each poll runs under the wait deadline, so a slow instance cannot drag the
// wait past it: the request is cancelled, not waited out.
func TestPollRespectsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(700 * time.Millisecond)
		fmt.Fprint(w, `{"id":1,"name":"SeriesSearch","status":"queued"}`)
	}))
	defer srv.Close()

	start := time.Now()
	_, cerr := run(t, "sonarr", "command", "wait", "--id", "1",
		"--timeout", "100ms", "--interval", "10ms",
		"--url", srv.URL, "--apikey", "test-only")
	elapsed := time.Since(start)

	if cerr == nil {
		t.Fatal("the wait did not time out")
	}
	if cerr.Code != output.CodeTransportTimeout || cerr.Exit != output.ExitTransport {
		t.Errorf("code = %s exit = %d, want TRANSPORT_TIMEOUT / 3", cerr.Code, cerr.Exit)
	}
	if elapsed > 600*time.Millisecond {
		t.Errorf("the wait ran %s past a 100ms deadline: the poll is not under it", elapsed)
	}
}

// A wait that expires between polls is a timeout too, and says the command is
// still running rather than claiming an outcome it never saw.
func TestWaitTimesOutBetweenPolls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":4,"name":"SeriesSearch","status":"started"}`)
	}))
	defer srv.Close()

	_, cerr := run(t, "sonarr", "command", "wait", "--id", "4",
		"--timeout", "120ms", "--interval", "20ms",
		"--url", srv.URL, "--apikey", "test-only")
	if cerr == nil || cerr.Code != output.CodeTransportTimeout {
		t.Fatalf("code = %+v, want TRANSPORT_TIMEOUT", cerr)
	}
	if !strings.Contains(cerr.Message, "still running on the instance") {
		t.Errorf("message = %q", cerr.Message)
	}
	if !strings.Contains(cerr.Hint, "command wait --id 4") {
		t.Errorf("hint = %q", cerr.Hint)
	}
}
