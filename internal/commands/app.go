// Package commands wires the cobra command tree. The first positional is the
// app (`radarr` or `sonarr`), always required, no default: two sub-roots are
// built from the same group factories, each handed an appSpec that supplies the
// flag names and query keys that differ between the two APIs. Shared groups are
// written once; the difference is a table, not a second implementation.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/corinthian/arrctl/internal/xduration"
	"github.com/corinthian/arrctl/internal/xhttp"
)

// Version is the binary version, stamped into the User-Agent and `version`. A
// var, not a const, so build.sh can overwrite it with -ldflags -X at build
// time; this literal is the fallback when no other source (positional arg,
// $ARRCTL_BUILD_VERSION, an exact git tag) supplies one.
var Version = "0.2.0"

// GlobalFlags holds the persistent flags bound on the root command.
type GlobalFlags struct {
	URL        string
	APIKey     string
	Insecure   bool
	Timeout    string
	ConfigPath string
	APIKeyIn   string

	Page     int
	PageSize int
	SortKey  string
	SortDir  string
	All      bool

	Confirm bool
	LLM     bool

	Raw    bool
	NDJSON bool
	Terse  bool
}

// App is the shared runtime context handed to every command factory. Target is
// resolved from the sub-root the invoked command sits under, so a group file
// never has to know which app it was built for beyond its appSpec.
type App struct {
	Flags  *GlobalFlags
	Out    *output.Writer
	Loaded *config.Loaded
	Target config.App
	Cfg    *config.AppConfig
	Client *client.Client

	// insecureSet records whether --insecure was actually typed. Without it an
	// untouched flag's false default would override `insecure = true` from the
	// config file, silently re-enabling TLS verification against an instance
	// whose certificate is known-bad.
	insecureSet bool

	// timeoutSet records whether --timeout was actually typed (cmd.Flags().
	// Changed("timeout")). Without it an explicitly empty --timeout "" is
	// indistinguishable from an untouched flag, and the empty value must be an
	// error rather than falling through to env or file.
	timeoutSet bool

	// invocationCtx is cmd.Context(), set from PersistentPreRunE. main.go calls
	// commands.Execute(), not ExecuteContext(), so in production this is always
	// context.Background() — no signal handler installs a deadline or a
	// cancellation on it. ctx() falls back to context.Background() when unset,
	// which covers every test that builds an App directly.
	invocationCtx context.Context
}

// NewApp builds an App with a default JSON writer; build() finishes setup in
// PersistentPreRunE once flags are parsed and the target app is known.
func NewApp() *App {
	return &App{Flags: &GlobalFlags{}, Out: output.New(os.Stdout, output.FormatJSON)}
}

// setTarget records which instance the invoked command targets. Config is NOT
// resolved here: a group command with no verb, or a command that fails its own
// flag validation, must report that usage error rather than a config error it
// would never have reached. The client is built on first use instead.
func (a *App) setTarget(target config.App) { a.Target = target }

// build resolves config for the target app and constructs the HTTP client. It
// is idempotent and called lazily, from the first request a command makes.
func (a *App) build(target config.App) *output.CLIError {
	if a.Client != nil {
		return nil
	}
	a.Target = target
	f := config.Flags{
		URL:         a.Flags.URL,
		APIKey:      a.Flags.APIKey,
		Insecure:    a.Flags.Insecure,
		InsecureSet: a.insecureSet,
		Timeout:     a.Flags.Timeout,
		TimeoutSet:  a.timeoutSet,
		ConfigPath:  a.Flags.ConfigPath,
	}
	cfg, err := config.Resolve(target, f, a.Loaded)
	if err != nil {
		return output.NewError(configErrorCode(err), err.Error())
	}
	a.Cfg = cfg
	a.Client = client.New(client.Config{
		App:       string(target),
		BaseURL:   cfg.URL,
		APIKey:    cfg.APIKey,
		Insecure:  cfg.Insecure,
		Timeout:   cfg.Timeout,
		Version:   Version,
		Placement: client.KeyPlacement(a.Flags.APIKeyIn),
		KeyHint:   target.KeyHint(),
	})
	return nil
}

// configErrorCode maps a config.Resolve error to its exit-code family. A
// rejected --timeout is the caller's usage mistake, BAD_REQUEST; a rejected
// $ARRCTL_TIMEOUT or config-file timeout is a config problem, BAD_CONFIG, like
// every other config.Resolve failure.
func configErrorCode(err error) string {
	var de *xduration.Error
	if errors.As(err, &de) && de.Source == "--timeout" {
		return output.CodeBadRequest
	}
	return output.CodeBadConfig
}

func (a *App) resolveFormat() output.Format {
	switch {
	case a.Flags.Raw:
		return output.FormatRaw
	case a.Flags.NDJSON:
		return output.FormatNDJSON
	case a.Flags.Terse:
		return output.FormatTerse
	default:
		return output.FormatJSON
	}
}

// meta builds the per-call metadata block, with meta.app always filled in.
func (a *App) meta(res *client.Result) *output.Meta {
	return &output.Meta{
		App:        string(a.Target),
		Endpoint:   res.Endpoint,
		DurationMS: res.DurationMS,
		Pagination: res.Pagination,
	}
}

// emit runs a client result through the writer. It is the single success path.
func (a *App) emit(res *client.Result, terse string) error {
	m := a.meta(res)
	if terse == "" && a.Out.Format == output.FormatTerse {
		terse = summarize(res.Data)
	}
	return a.Out.Emit(&output.Result{Data: res.Data, Meta: m, Terse: terse})
}

// emitRaw emits arrctl-generated data (not an app response), e.g. `commands`.
func (a *App) emitLocal(v interface{}, terse string) error {
	b, err := json.Marshal(v)
	if err != nil {
		return output.NewError(output.CodeParseError, err.Error())
	}
	return a.Out.Emit(&output.Result{Data: b, Terse: terse})
}

// ctx returns the invocation's context, set from cmd.Context() in
// PersistentPreRunE, falling back to context.Background() when unset (every
// test that builds an App directly, and any code path reached before
// PersistentPreRunE runs). The deadline, if any, is the HTTP client timeout;
// cancellation is otherwise unreachable in production, since main.go calls
// Execute(), not ExecuteContext(), and no tool installs a signal handler.
func (a *App) ctx() context.Context {
	if a.invocationCtx != nil {
		return a.invocationCtx
	}
	return context.Background()
}

func (a *App) do(method, path string, opts client.Options) (*client.Result, error) {
	return a.doCtx(a.ctx(), method, path, opts)
}

// doCtx is do under a caller-supplied context, for the one caller that owns a
// deadline of its own: the command poller, whose wait timeout has to bound each
// poll rather than only the gap between them. Nothing else plumbs a context.
func (a *App) doCtx(ctx context.Context, method, path string, opts client.Options) (*client.Result, error) {
	if cerr := a.build(a.Target); cerr != nil {
		return nil, cerr
	}
	res, cerr := a.Client.Do(ctx, method, path, opts)
	if cerr != nil {
		return nil, cerr
	}
	return res, nil
}

func (a *App) get(path string, opts client.Options) (*client.Result, error) {
	return a.do(http.MethodGet, path, opts)
}

func (a *App) post(path string, opts client.Options) (*client.Result, error) {
	return a.do(http.MethodPost, path, opts)
}

func (a *App) put(path string, opts client.Options) (*client.Result, error) {
	return a.do(http.MethodPut, path, opts)
}

func (a *App) del(path string, opts client.Options) (*client.Result, error) {
	return a.do(http.MethodDelete, path, opts)
}

// confirmed reports whether a gated mutation is authorized.
func (a *App) confirmed() bool {
	return a.Flags.Confirm || os.Getenv("ARRCTL_CONFIRM") == "1"
}

// gate refuses a confirm-gated action that was not confirmed. `what` names the
// action in the refusal message.
func (a *App) gate(what string) *output.CLIError {
	if a.confirmed() {
		return nil
	}
	return output.ConfirmRequired(what)
}

// resolvePayload is the single entry point every payload-taking command uses:
// exactly one of --payload / --payload-file, the latter reading stdin on "-".
//
// The caller's bytes are what goes on the wire. They are validated, not
// re-encoded: decoding to check the JSON and then marshalling the result back
// rewrote any integer wider than a float64, so a payload naming a movie by a
// large id acted on a different record.
func resolvePayload(payload, payloadFile string) (json.RawMessage, error) {
	if payload != "" && payloadFile != "" {
		return nil, output.UsageError("--payload and --payload-file are mutually exclusive")
	}
	if payloadFile != "" {
		data, err := readPayloadFile(payloadFile)
		if err != nil {
			return nil, output.UsageError("reading --payload-file: " + err.Error())
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return nil, output.UsageError("--payload-file is empty")
		}
		return decodeJSON(data, "--payload-file")
	}
	if payload == "" {
		return nil, output.UsageError("missing required --payload (or --payload-file) JSON body")
	}
	return decodeJSON([]byte(payload), "--payload")
}

func readPayloadFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

// decodeJSON validates that data is exactly one JSON value and hands the
// original bytes back. UseNumber keeps the check itself from being the thing
// that loses precision, and the strict single-value rule is the same one
// responses are held to: an object followed by anything is a mistake, whether
// or not the trailing text happens to parse.
func decodeJSON(data []byte, source string) (json.RawMessage, error) {
	var probe interface{}
	if err := xhttp.DecodeOne(data, &probe); err != nil {
		return nil, output.UsageError("invalid " + source + " JSON: " + err.Error())
	}
	return json.RawMessage(data), nil
}

// payloadObject reads a payload's top-level keys, rejecting anything that is
// not a JSON object. Commands that merge a flag into the payload need the keys;
// the object requirement is the pre-existing rule.
func payloadObject(raw json.RawMessage, what string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' ||
		json.Unmarshal(raw, &m) != nil {
		return nil, output.UsageError(what)
	}
	return m, nil
}

// summarize derives a one-line human summary from a response body, used by
// --terse when a command supplies no explicit line.
func summarize(data json.RawMessage) string {
	if len(data) == 0 {
		return "ok"
	}
	var arr []json.RawMessage
	if json.Unmarshal(data, &arr) == nil {
		return plural(len(arr), "record")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return ""
	}
	if recs, ok := obj["records"]; ok {
		var rows []json.RawMessage
		_ = json.Unmarshal(recs, &rows)
		total := 0
		if t, ok := obj["totalRecords"]; ok {
			_ = json.Unmarshal(t, &total)
		}
		if total > len(rows) {
			return plural(len(rows), "record") + " of " + itoa(total)
		}
		return plural(len(rows), "record")
	}
	// A named object (system status, a movie, a command record).
	for _, k := range []string{"title", "name", "appName", "version"} {
		if raw, ok := obj[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				if k == "appName" {
					if vraw, ok := obj["version"]; ok {
						var v string
						if json.Unmarshal(vraw, &v) == nil && v != "" {
							return s + " " + v
						}
					}
				}
				return s
			}
		}
	}
	return ""
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return itoa(n) + " " + noun + "s"
}
