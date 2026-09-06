// Package output defines the JSON envelope, meta block, exit-code mapping, and
// the formatters (default JSON, --raw, --ndjson, --terse) every command emits
// through. It is the single contract: commands never write to stdout directly.
// Errors go to stdout in the envelope; stderr stays unused.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/corinthian/arrctl/internal/rawjson"
)

// ExitCode is the process exit status. Stable per the spec's error model.
type ExitCode int

const (
	ExitOK        ExitCode = 0 // success
	ExitUser      ExitCode = 1 // bad flags, missing args, invalid config, confirm refused
	ExitArr       ExitCode = 2 // Radarr/Sonarr returned non-2xx
	ExitTransport ExitCode = 3 // DNS, TLS, timeout, refused
	ExitInternal  ExitCode = 4 // internal arrctl error, or a response from the instance arrctl could not decode
)

// Error-code enum values (the error envelope's `code` field). Closed set.
const (
	// CodeBadRequest is the usage family: anything the caller typed wrong —
	// unknown flag, unknown/missing app, missing subcommand, invalid required
	// flag value.
	CodeBadRequest = "BAD_REQUEST"
	// CodeBadConfig is the config family only: unreadable/invalid config file,
	// missing url or api key. Never a usage error.
	CodeBadConfig = "BAD_CONFIG"
	// CodeConfirmRequired is a refused confirm gate. Usage family, exit 1.
	CodeConfirmRequired = "CONFIRM_REQUIRED"

	// One ARR_ prefix for both apps; meta.app says which.
	CodeArrAuthFailed = "ARR_AUTH_FAILED"
	CodeArrValidation = "ARR_VALIDATION"
	CodeArrNotFound   = "ARR_NOT_FOUND"
	CodeArrError      = "ARR_ERROR"

	CodeTransportDNS     = "TRANSPORT_DNS"
	CodeTransportTLS     = "TRANSPORT_TLS"
	CodeTransportTimeout = "TRANSPORT_TIMEOUT"
	CodeTransportRefused = "TRANSPORT_REFUSED"
	CodeTransportFailed  = "TRANSPORT_FAILED"

	// CodeParseError is caller input and internal marshal failures. Never a
	// response body — see CodeDecodeError.
	CodeParseError = "PARSE_ERROR"
	// CodeDecodeError fires when the instance sends malformed JSON or an
	// oversize body.
	CodeDecodeError       = "DECODE_ERROR"
	CodePaginationRunaway = "PAGINATION_RUNAWAY"
)

// codeExit is the central code -> exit-class mapping: the one place the pairing
// lives, so a new call site cannot invent its own exit number for an existing
// code (traktctl Bug 2). A code absent from this map is a bug and exits
// ExitInternal.
var codeExit = map[string]ExitCode{
	CodeBadRequest:        ExitUser,
	CodeBadConfig:         ExitUser,
	CodeConfirmRequired:   ExitUser,
	CodePaginationRunaway: ExitUser,

	CodeArrAuthFailed: ExitArr,
	CodeArrValidation: ExitArr,
	CodeArrNotFound:   ExitArr,
	CodeArrError:      ExitArr,

	CodeTransportDNS:     ExitTransport,
	CodeTransportTLS:     ExitTransport,
	CodeTransportTimeout: ExitTransport,
	CodeTransportRefused: ExitTransport,
	CodeTransportFailed:  ExitTransport,

	CodeParseError:  ExitInternal,
	CodeDecodeError: ExitInternal,
}

// ExitForCode returns the exit class an error code maps to. Unknown codes are
// ExitInternal: emitting a code outside the enumeration is an arrctl bug.
func ExitForCode(code string) ExitCode {
	if e, ok := codeExit[code]; ok {
		return e
	}
	return ExitInternal
}

// Codes returns the closed code set, for tests and documentation.
func Codes() []string {
	out := make([]string, 0, len(codeExit))
	for c := range codeExit {
		out = append(out, c)
	}
	return out
}

// UsageError builds a usage-family (BAD_REQUEST) error. The exit class comes
// from codeExit, so no call site names an exit number.
func UsageError(msg string) *CLIError {
	return &CLIError{Code: CodeBadRequest, Message: msg, Exit: ExitForCode(CodeBadRequest)}
}

// UsageErrorHint is UsageError with the recovery hint filled in.
func UsageErrorHint(msg, hint string) *CLIError {
	e := UsageError(msg)
	e.Hint = hint
	return e
}

// ConfirmRequired builds the refusal returned by a gated mutation run without
// --confirm / ARRCTL_CONFIRM=1.
func ConfirmRequired(what string) *CLIError {
	return &CLIError{
		Code:    CodeConfirmRequired,
		Message: what + " is confirm-gated and no confirmation was given; nothing was changed",
		Hint:    "re-run with --confirm, or set ARRCTL_CONFIRM=1",
		Exit:    ExitForCode(CodeConfirmRequired),
	}
}

// Envelope is the standard success/error wrapper.
type Envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *ErrorBody      `json:"error,omitempty"`
	Meta  *Meta           `json:"meta,omitempty"`
}

// ErrorBody is the structured error payload.
type ErrorBody struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

// Meta is the per-call metadata block. App is always present on a call that
// targeted an instance, so a caller can tell the two apart from output alone.
type Meta struct {
	App        string      `json:"app,omitempty"`
	Endpoint   string      `json:"endpoint,omitempty"`
	DurationMS int64       `json:"duration_ms"`
	Pagination *Pagination `json:"pagination,omitempty"`
}

// Pagination mirrors the Servarr paged-response envelope fields.
type Pagination struct {
	Page         int `json:"page"`
	PageSize     int `json:"page_size"`
	TotalRecords int `json:"total_records"`
}

// CLIError is a typed error carrying everything needed to build an error
// envelope and pick an exit code. Every failure path returns one of these.
type CLIError struct {
	Code       string
	Message    string
	HTTPStatus int
	Hint       string
	Exit       ExitCode
	App        string
	Endpoint   string
	DurationMS int64
}

func (e *CLIError) Error() string { return e.Message }

// NewError builds a CLIError whose exit class comes from the central map.
func NewError(code, msg string) *CLIError {
	return &CLIError{Code: code, Message: msg, Exit: ExitForCode(code)}
}

// Format selects how command data is rendered.
type Format int

const (
	FormatJSON   Format = iota // default: full envelope, pretty
	FormatRaw                  // app response untouched
	FormatNDJSON               // one object per line (list commands)
	FormatTerse                // one-line plain-English summary
)

// Writer renders results and errors to the given stream honoring the format.
type Writer struct {
	Out    io.Writer
	Format Format
}

// New returns a Writer.
func New(out io.Writer, f Format) *Writer {
	return &Writer{Out: out, Format: f}
}

// Result is a successful command outcome ready to render.
type Result struct {
	Data json.RawMessage // raw app body (object or array)
	Meta *Meta
	// Terse is an optional one-line human summary used by FormatTerse. When
	// empty under --terse, Writer falls back to a compact JSON line.
	Terse string
}

// Emit renders a successful result per the configured format. An empty body
// (e.g. HTTP 204) is normalized to nil, and the JSON path then emits an
// explicit `data: null` — the envelope the spec, the README and the --llm
// schema string all promise. Envelope.Data keeps its omitempty because the
// struct is shared with EmitError: dropping it would give every error envelope
// a "data":null it has no business carrying, so the null is supplied here
// instead. A write failure is returned as a typed CLIError so it stays inside
// the error model.
func (w *Writer) Emit(r *Result) error {
	if len(r.Data) == 0 {
		r.Data = nil
	}
	var err error
	switch w.Format {
	case FormatRaw:
		err = w.writeRaw(r.Data)
	case FormatNDJSON:
		err = w.writeNDJSON(r.Data)
	case FormatTerse:
		err = w.writeTerse(r)
	default:
		data := r.Data
		if len(data) == 0 {
			data = json.RawMessage("null")
		}
		err = w.writeJSON(Envelope{OK: true, Data: data, Meta: r.Meta})
	}
	if err != nil {
		return NewError(CodeParseError, "writing output: "+err.Error())
	}
	return nil
}

// EmitError renders a CLIError as an error envelope on stdout and returns the
// exit code. Errors are always machine-readable, --raw included: there is no
// upstream body to pass through on a failure.
func (w *Writer) EmitError(e *CLIError) ExitCode {
	env := Envelope{
		OK: false,
		Error: &ErrorBody{
			Code:       e.Code,
			Message:    e.Message,
			HTTPStatus: e.HTTPStatus,
			Hint:       e.Hint,
		},
	}
	if e.App != "" || e.Endpoint != "" || e.DurationMS != 0 {
		env.Meta = &Meta{App: e.App, Endpoint: e.Endpoint, DurationMS: e.DurationMS}
	}
	_ = w.writeJSON(env)
	return e.exitOrInternal()
}

// exitOrInternal guards against a zero-value Exit silently meaning success: an
// error that never set one derives it from its code via the central map, and an
// unmapped code lands on ExitInternal.
func (e *CLIError) exitOrInternal() ExitCode {
	if e.Exit == ExitOK {
		return ExitForCode(e.Code)
	}
	return e.Exit
}

func (w *Writer) writeJSON(v interface{}) error {
	enc := json.NewEncoder(w.Out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeRaw passes the app body through, indented on the bytes rather than
// decoded and re-encoded. Key order and number literals therefore survive: a
// tmdbId past 2^53 came back as a different number through the old
// interface{} round trip, and the instance's own key order was sorted away.
//
// There is no verbatim-bytes fallback for a body that does not parse. An
// invalid body is a failure, and reporting it as one beats printing it and
// exiting 0.
func (w *Writer) writeRaw(data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	out, err := rawjson.Indent(data)
	if err != nil {
		return err
	}
	// json.Indent copies trailing whitespace from the source through to the
	// output, so a body arriving with its own newline would otherwise print a
	// blank line after it. The old encoder emitted exactly one newline and so
	// does this.
	_, err = w.Out.Write(append(bytes.TrimRight(out, " \t\r\n"), '\n'))
	return err
}

// writeNDJSON emits one line per element of a top-level array. A Servarr paged
// object is unwrapped to its `records` array first, so --ndjson works the same
// on paged and unpaged lists. Anything else is emitted as a single line.
func (w *Writer) writeNDJSON(data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	if recs := PagedRecords(data); recs != nil {
		data = recs
	}
	arr, ok := rawjson.SplitArray(data)
	if !ok {
		return w.writeCompactLine(data)
	}
	for _, el := range arr {
		if err := w.writeCompactLine(el); err != nil {
			return err
		}
	}
	return nil
}

// PagedRecords returns the `records` array of a Servarr paged response, or nil
// when the body is not one.
func PagedRecords(data json.RawMessage) json.RawMessage {
	var probe struct {
		Records json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil
	}
	return probe.Records
}

// writeCompactLine emits one row, compacted on the bytes. Nothing is decoded,
// so nothing is re-encoded: HTML escaping cannot be reintroduced and integer
// literals cannot be rewritten, which is what SetEscapeHTML(false) and the
// UseNumber decoding were each protecting against separately.
func (w *Writer) writeCompactLine(raw json.RawMessage) error {
	line, err := rawjson.Compact(raw)
	if err != nil {
		return err
	}
	_, err = w.Out.Write(append(line, '\n'))
	return err
}

func (w *Writer) writeTerse(r *Result) error {
	if r.Terse != "" {
		_, err := fmt.Fprintln(w.Out, r.Terse)
		return err
	}
	if len(r.Data) == 0 {
		_, err := fmt.Fprintln(w.Out, "ok")
		return err
	}
	return w.writeCompactLine(r.Data)
}
