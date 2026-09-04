package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

func init() { Register("command", newCommandGroup) }

// commandRecord is the subset of Servarr's CommandResource arrctl needs. The
// full record is passed through untouched; this is only for polling and terse.
type commandRecord struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Result  string `json:"result"`
}

// Terminal command states. Everything else means still running.
const (
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusAborted   = "aborted"
	statusCancelled = "cancelled"
)

func isTerminal(status string) bool {
	switch strings.ToLower(status) {
	case statusCompleted, statusFailed, statusAborted, statusCancelled:
		return true
	}
	return false
}

// runAllowlist is the set of command names `command run` accepts without
// --confirm. Everything else is confirm-required, because neither OpenAPI
// document enumerates the command-name set: an unrecognised name could be
// anything, including one that starts downloads or rewrites the library.
//
// These are the names behind the search sugar, with two deliberate omissions
// from the spec's list: MissingMoviesSearch / MissingEpisodeSearch and RssSync.
// Both are library-wide and grab whatever they find — the missing search across
// every monitored record with no file, RssSync across every feed. Their sugar
// verbs (`wanted search`, `rss sync`) are confirm-gated for that reason, and
// leaving the raw `command run` path to the same body open would make the gate
// decorative. Targeted search/refresh/rescan stay open: they act on ids the
// caller named.
func runAllowlist(s *appSpec) map[string]bool {
	names := map[string]bool{
		s.SearchCommand:  true,
		s.RefreshCommand: true,
		s.RescanCommand:  true,
	}
	if s.Name == config.Sonarr {
		names["SeasonSearch"] = true
		names["EpisodeSearch"] = true
	}
	return names
}

func allowlistNames(s *appSpec) []string {
	out := []string{}
	for n := range runAllowlist(s) {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func newCommandGroup(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "command", Short: "Asynchronous command dispatch"}
	root.AddCommand(a.simpleGet("list", "List recent and running commands", "/command"))
	root.AddCommand(a.getByID("get", "Get one command record", "/command", "", "command id"))
	root.AddCommand(a.commandRun(s))
	root.AddCommand(a.commandCancel())
	root.AddCommand(a.commandWaitCmd())
	return root
}

// commandRun posts to /command. The body is {"name": NAME} merged with
// --payload, so `--name MoviesSearch --payload '{"movieIds":[37]}'` produces
// exactly the documented body.
func (a *App) commandRun(s *appSpec) *cobra.Command {
	var name, payload, payloadFile string
	var wait bool
	var waitTimeout, waitInterval time.Duration
	c := &cobra.Command{
		Use:   "run",
		Short: "Dispatch a command by name",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "POST " + client.APIPrefix + "/command",
			"examples": "arrctl " + string(s.Name) + " command run --name " + s.RefreshCommand + " --payload '{}'\n" +
				"arrctl " + string(s.Name) + " command run --name RssSync",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return output.UsageErrorHint("missing required --name",
					"no-confirm names on "+string(s.Name)+": "+strings.Join(allowlistNames(s), ", ")+
						"; any other name needs --confirm")
			}
			if !runAllowlist(s)[name] {
				if cerr := a.gate("running the un-allowlisted command " + name); cerr != nil {
					cerr.Hint = "the command-name set is not enumerated in the API document, so only " +
						strings.Join(allowlistNames(s), ", ") + " run without --confirm"
					return cerr
				}
			}
			body := map[string]interface{}{}
			if payload != "" || payloadFile != "" {
				v, err := resolvePayload(payload, payloadFile)
				if err != nil {
					return err
				}
				m, ok := v.(map[string]interface{})
				if !ok {
					return output.UsageError("--payload for `command run` must be a JSON object")
				}
				body = m
			}
			body["name"] = name

			res, err := a.post("/command", client.Options{Body: body})
			if err != nil {
				return err
			}
			if !wait {
				return a.emit(res, terseCommand(res.Data))
			}
			return a.waitAndEmit(res.Data, waitTimeout, waitInterval)
		},
	}
	c.Flags().StringVar(&name, "name", "", "command name, e.g. "+s.RefreshCommand)
	addPayloadFlags(c, &payload, &payloadFile)
	addWaitFlags(c, &wait, &waitTimeout, &waitInterval)
	return c
}

func (a *App) commandCancel() *cobra.Command {
	c := &cobra.Command{
		Use:         "cancel",
		Short:       "Cancel a queued or running command",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "DELETE " + client.APIPrefix + "/command/{id}"},
	}
	id := idFlag(c, "command id")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		res, err := a.del("/command/"+itoa(*id), client.Options{})
		if err != nil {
			return err
		}
		return a.emit(res, "cancelled")
	}
	return c
}

// commandWaitCmd polls GET /command/{id} to a terminal status.
//
// Flag note: --timeout here is a LOCAL flag that shadows the root's HTTP
// --timeout, matching the spec's `command wait --id N [--timeout 10m]`. Each
// poll is a short GET, so the HTTP timeout keeps its 30s default on this
// command and cannot be set from the command line. The sugar verbs avoid the
// collision entirely by spelling theirs --wait-timeout.
func (a *App) commandWaitCmd() *cobra.Command {
	var timeout, interval time.Duration
	c := &cobra.Command{
		Use:         "wait",
		Short:       "Poll a command until it finishes",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET " + client.APIPrefix + "/command/{id} (polled)"},
	}
	id := idFlag(c, "command id")
	c.Flags().DurationVar(&timeout, "timeout", 10*time.Minute,
		"give up after this long (shadows the global HTTP --timeout on this command)")
	c.Flags().DurationVar(&interval, "interval", 2*time.Second, "seconds between polls")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		res, rec, err := a.pollCommand(*id, timeout, interval)
		if err != nil {
			return err
		}
		return a.emit(res, terseRecord(rec))
	}
	return c
}

// addWaitFlags registers the --wait trio used by `command run` and the sugar.
// The duration flag is --wait-timeout, not --timeout, so it never shadows the
// global HTTP timeout.
func addWaitFlags(c *cobra.Command, wait *bool, timeout, interval *time.Duration) {
	c.Flags().BoolVar(wait, "wait", false, "poll the command record until it finishes")
	c.Flags().DurationVar(timeout, "wait-timeout", 10*time.Minute, "with --wait, give up after this long")
	c.Flags().DurationVar(interval, "wait-interval", 2*time.Second, "with --wait, seconds between polls")
}

// waitAndEmit takes the POST /command response, extracts the id, and polls.
func (a *App) waitAndEmit(posted json.RawMessage, timeout, interval time.Duration) error {
	var rec commandRecord
	if err := json.Unmarshal(posted, &rec); err != nil || rec.ID == 0 {
		return output.NewError(output.CodeParseError,
			"the command was dispatched but its response carried no id, so --wait cannot poll it")
	}
	res, final, err := a.pollCommand(rec.ID, timeout, interval)
	if err != nil {
		return err
	}
	return a.emit(res, terseRecord(final))
}

// pollCommand polls until the record reaches a terminal status or the deadline
// passes. A timeout is TRANSPORT_TIMEOUT: the command may still be running on
// the instance, and the message says so.
func (a *App) pollCommand(id int, timeout, interval time.Duration) (*client.Result, *commandRecord, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := a.get("/command/"+itoa(id), client.Options{})
		if err != nil {
			return nil, nil, err
		}
		var rec commandRecord
		if uerr := json.Unmarshal(res.Data, &rec); uerr != nil {
			return nil, nil, output.NewError(output.CodeParseError,
				"cannot read the command record: "+uerr.Error())
		}
		if isTerminal(rec.Status) {
			return res, &rec, nil
		}
		if time.Now().Add(interval).After(deadline) {
			return nil, nil, &output.CLIError{
				Code: output.CodeTransportTimeout,
				Message: fmt.Sprintf("command %d (%s) was still %s after %s; it is still running on the instance",
					rec.ID, rec.Name, rec.Status, timeout),
				Hint: "poll it again with `command wait --id " + itoa(rec.ID) + "` or read it with `command get --id " + itoa(rec.ID) + "`",
				Exit: output.ExitTransport, App: string(a.Target),
			}
		}
		time.Sleep(interval)
	}
}

func terseCommand(data json.RawMessage) string {
	var rec commandRecord
	if json.Unmarshal(data, &rec) != nil {
		return ""
	}
	return terseRecord(&rec)
}

func terseRecord(rec *commandRecord) string {
	if rec == nil || rec.Name == "" {
		return ""
	}
	s := fmt.Sprintf("%s #%d %s", rec.Name, rec.ID, rec.Status)
	if rec.Message != "" {
		s += ": " + rec.Message
	}
	return s
}
