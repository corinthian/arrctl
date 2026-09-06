package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Every code in the closed set must map to exactly the exit class the spec's
// error-model table names. This is the one place exit numbers are asserted.
func TestCodeExitMap(t *testing.T) {
	want := map[string]ExitCode{
		CodeBadRequest:        ExitUser,
		CodeBadConfig:         ExitUser,
		CodeConfirmRequired:   ExitUser,
		CodePaginationRunaway: ExitUser,
		CodeArrAuthFailed:     ExitArr,
		CodeArrValidation:     ExitArr,
		CodeArrNotFound:       ExitArr,
		CodeArrError:          ExitArr,
		CodeTransportDNS:      ExitTransport,
		CodeTransportTLS:      ExitTransport,
		CodeTransportTimeout:  ExitTransport,
		CodeTransportRefused:  ExitTransport,
		CodeTransportFailed:   ExitTransport,
		CodeParseError:        ExitInternal,
		CodeDecodeError:       ExitInternal,
	}
	for code, exp := range want {
		if got := ExitForCode(code); got != exp {
			t.Errorf("ExitForCode(%s) = %d, want %d", code, got, exp)
		}
	}
	if len(Codes()) != len(want) {
		t.Errorf("code set drifted: map has %d codes, test names %d", len(Codes()), len(want))
	}
}

// An unmapped code is an arrctl bug, and must never read as success.
func TestUnknownCodeIsInternal(t *testing.T) {
	if got := ExitForCode("NOT_A_REAL_CODE"); got != ExitInternal {
		t.Errorf("unknown code exit = %d, want %d", got, ExitInternal)
	}
}

// An error built without an explicit Exit still derives one from its code, so a
// zero value cannot silently mean success.
func TestZeroExitDerivesFromCode(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	got := w.EmitError(&CLIError{Code: CodeArrNotFound, Message: "gone"})
	if got != ExitArr {
		t.Fatalf("exit = %d, want %d", got, ExitArr)
	}
}

func TestSuccessEnvelopeShape(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	err := w.Emit(&Result{
		Data: json.RawMessage(`{"version":"6.3.0"}`),
		Meta: &Meta{App: "radarr", Endpoint: "/api/v3/system/status", DurationMS: 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
		Meta *Meta           `json:"meta"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Error("ok = false, want true")
	}
	if env.Meta == nil || env.Meta.App != "radarr" {
		t.Errorf("meta.app = %+v, want radarr", env.Meta)
	}
	if env.Meta.Endpoint != "/api/v3/system/status" {
		t.Errorf("meta.endpoint = %q", env.Meta.Endpoint)
	}
}

// Errors go to stdout in the envelope, per the corrected component contract.
func TestErrorEnvelopeShape(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	exit := w.EmitError(&CLIError{
		Code: CodeArrAuthFailed, Message: "sonarr returned 401 Unauthorized",
		HTTPStatus: 401, Hint: "store the key in the Keychain",
		Exit: ExitArr, App: "sonarr", Endpoint: "/api/v3/series",
	})
	if exit != ExitArr {
		t.Fatalf("exit = %d, want 2", exit)
	}
	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.OK {
		t.Error("ok = true on an error envelope")
	}
	if env.Error == nil || env.Error.Code != CodeArrAuthFailed || env.Error.HTTPStatus != 401 {
		t.Errorf("error body = %+v", env.Error)
	}
	if env.Meta == nil || env.Meta.App != "sonarr" {
		t.Errorf("meta.app missing on error: %+v", env.Meta)
	}
}

// --ndjson unwraps a Servarr paged body to one line per record.
func TestNDJSONUnwrapsPagedBody(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatNDJSON)
	body := `{"page":1,"pageSize":2,"totalRecords":2,"records":[{"id":1},{"id":2}]}`
	if err := w.Emit(&Result{Data: json.RawMessage(body)}); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Count(bytes.TrimSpace(buf.Bytes()), []byte("\n")) + 1
	if lines != 2 {
		t.Errorf("got %d lines, want 2:\n%s", lines, buf.String())
	}
}

func TestRawPassesBodyThrough(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatRaw)
	if err := w.Emit(&Result{Data: json.RawMessage(`[{"id":1}]`), Meta: &Meta{App: "radarr"}}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte(`"ok"`)) {
		t.Errorf("--raw emitted the envelope:\n%s", buf.String())
	}
}

// An empty body (HTTP 204) must emit a null-data success envelope, not fail.
func TestEmptyBodyEmitsNullData(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	if err := w.Emit(&Result{Data: nil, Meta: &Meta{App: "radarr"}}); err != nil {
		t.Fatal(err)
	}
	var env map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not valid JSON: %s", buf.String())
	}
	if env["ok"] != true {
		t.Errorf("ok = %v", env["ok"])
	}
}

func TestConfirmRequiredIsUserExit(t *testing.T) {
	e := ConfirmRequired("deleting a film")
	if e.Code != CodeConfirmRequired || e.Exit != ExitUser {
		t.Errorf("got code=%s exit=%d, want CONFIRM_REQUIRED / 1", e.Code, e.Exit)
	}
	if e.Hint == "" {
		t.Error("confirm refusal carries no recovery hint")
	}
}

// The documented envelope is {ok, data, meta} with data object|array|null, and
// both the Emit comment and the --llm schema string promise `data: null` on an
// empty body. omitempty on a shared struct field meant the key was dropped
// instead, so a 204 emitted an envelope with no data key at all — a consumer
// reading env["data"] got "absent", not "null".
func TestEmptyDataEmitsNull(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	if err := w.Emit(&Result{Data: nil, Meta: &Meta{App: "radarr"}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"data": null`)) {
		t.Errorf("no explicit null data in the envelope:\n%s", buf.String())
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not valid JSON: %s", buf.String())
	}
	if _, ok := env["data"]; !ok {
		t.Error("the data key is absent, not null")
	}
}

// omitempty stays on Envelope.Data, because the struct is shared with
// EmitError: dropping it would give every error envelope a "data":null it has
// no business carrying.
func TestErrorEnvelopeHasNoDataKey(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatJSON)
	w.EmitError(&CLIError{Code: CodeArrNotFound, Message: "nope", Exit: ExitArr})
	var env map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("not valid JSON: %s", buf.String())
	}
	if _, ok := env["data"]; ok {
		t.Errorf("an error envelope carries a data key:\n%s", buf.String())
	}
}

// --raw is the pass-through format: what the instance sent is what the caller
// sees, indented. A decode-and-re-encode round trip sorted the keys and pushed
// every integer through a float64, so an id past 2^53 came back changed.
func TestRawPreservesKeyOrderAndBigIntegers(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatRaw)
	body := `{"zulu":1,"tmdbId":9007199254740993,"alpha":2}`
	if err := w.Emit(&Result{Data: json.RawMessage(body)}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "9007199254740993") {
		t.Errorf("the integer was rewritten:\n%s", got)
	}
	z, tm, a := strings.Index(got, "zulu"), strings.Index(got, "tmdbId"), strings.Index(got, "alpha")
	if !(z < tm && tm < a) {
		t.Errorf("key order was not preserved:\n%s", got)
	}
}

func TestNDJSONPreservesBigIntegers(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatNDJSON)
	if err := w.Emit(&Result{Data: json.RawMessage(`[{"id":9007199254740993},{"id":9007199254740994}]`)}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines:\n%s", len(lines), buf.String())
	}
	if lines[0] != `{"id":9007199254740993}` || lines[1] != `{"id":9007199254740994}` {
		t.Errorf("rows were rewritten:\n%s", buf.String())
	}
}

// A Servarr paged body splits on its `records` array, not on the envelope, so
// --ndjson reads the same on a paged and an unpaged list.
func TestNDJSONUnwrapsPagedRecords(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatNDJSON)
	body := `{"page":1,"pageSize":2,"totalRecords":2,"records":[{"id":1},{"id":2}]}`
	if err := w.Emit(&Result{Data: json.RawMessage(body)}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	if got != "{\"id\":1}\n{\"id\":2}" {
		t.Errorf("paged body did not unwrap to its records:\n%s", buf.String())
	}
}

func TestNDJSONOnBareArraySplitsDirectly(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatNDJSON)
	if err := w.Emit(&Result{Data: json.RawMessage(`[{"a":1},"two",3]`)}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	if got != "{\"a\":1}\n\"two\"\n3" {
		t.Errorf("array did not split into one row per element:\n%s", buf.String())
	}
}

func TestNDJSONOnNonArrayEmitsOneLine(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, FormatNDJSON)
	if err := w.Emit(&Result{Data: json.RawMessage(`{"version":"5.0"}`)}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\"version\":\"5.0\"}\n" {
		t.Errorf("got %q, want one compact line", buf.String())
	}
}

// A --raw body arriving with its own trailing newline prints one newline, not
// two: json.Indent copies trailing whitespace through, where the encoder this
// replaced always emitted exactly one.
func TestRawEmitsOneTrailingNewline(t *testing.T) {
	for _, body := range []string{`{"a":1}`, "{\"a\":1}\n", "{\"a\":1}  \n\n"} {
		var buf bytes.Buffer
		if err := New(&buf, FormatRaw).Emit(&Result{Data: json.RawMessage(body)}); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); !strings.HasSuffix(got, "}\n") || strings.HasSuffix(got, "\n\n") {
			t.Errorf("body %q printed %q", body, got)
		}
	}
}

// --raw used to print an unparseable body verbatim and exit 0, alone among the
// formats in doing so. Deleting that fallback makes it fail like the others: a
// body arrctl cannot read is a failure, and saying so beats printing it.
// Item 11 later moves the failure to response time and gives it its own code.
func TestRawOnAnInvalidBodyIsAnError(t *testing.T) {
	var buf bytes.Buffer
	err := New(&buf, FormatRaw).Emit(&Result{Data: json.RawMessage(`<html>not json</html>`)})
	if err == nil {
		t.Fatalf("an unparseable body was printed and reported as success: %q", buf.String())
	}
	cerr, ok := err.(*CLIError)
	if !ok {
		t.Fatalf("err = %T, want *CLIError", err)
	}
	if cerr.Code != CodeParseError {
		t.Errorf("code = %s, want PARSE_ERROR", cerr.Code)
	}
	if got := ExitForCode(cerr.Code); got != ExitInternal {
		t.Errorf("exit = %d, want 4", got)
	}
}

// failingWriter always errors, for exercising a write failure without a real
// broken pipe.
type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// A write failure on the error path is never silently discarded: it becomes a
// single plain-text stderr line and exit 4, the envelope is not retried.
func TestErrorPathWriteFailureExitsInternal(t *testing.T) {
	var stderr bytes.Buffer
	w := New(failingWriter{err: errWriteBoom}, FormatJSON)
	w.Err = &stderr
	exit := w.EmitError(&CLIError{
		Code: CodeArrAuthFailed, Message: "sonarr returned 401 Unauthorized",
		Exit: ExitArr, App: "sonarr",
	})
	if exit != ExitInternal {
		t.Errorf("exit = %d, want ExitInternal (4)", exit)
	}
	got := stderr.String()
	if !strings.Contains(got, "cannot write output") ||
		!strings.Contains(got, errWriteBoom.Error()) ||
		!strings.Contains(got, CodeArrAuthFailed) {
		t.Errorf("stderr = %q, want a line naming the io error and the original code", got)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("stderr = %q, want exactly one line", got)
	}
}

// The success path's existing write-failure handling (PARSE_ERROR, exit 4) is
// unchanged by the error-path fix.
func TestSuccessPathWriteFailureUnchanged(t *testing.T) {
	w := New(failingWriter{err: errWriteBoom}, FormatJSON)
	err := w.Emit(&Result{Data: json.RawMessage(`{"a":1}`)})
	cerr, ok := err.(*CLIError)
	if !ok || cerr.Code != CodeParseError || cerr.exitOrInternal() != ExitInternal {
		t.Fatalf("err = %+v, want PARSE_ERROR / exit 4", err)
	}
}

var errWriteBoom = errBoom{}

type errBoom struct{}

func (errBoom) Error() string { return "disk is full" }
