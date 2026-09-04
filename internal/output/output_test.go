package output

import (
	"bytes"
	"encoding/json"
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
		CodeParseError:        ExitInternal,
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
