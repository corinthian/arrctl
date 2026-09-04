package commands

import (
	"net/http"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	Register("queue", newQueueCmd)
	Register("history", newHistoryCmd)
	Register("wanted", newWantedCmd)
	Register("release", newReleaseCmd)
	Register("calendar", newCalendarCmd)
	Register("rename", newRenameCmd)
	Register("manualimport", newManualImportCmd)
	Register("parse", newParseCmd)
}

// newQueueCmd builds the `queue` group. list/details take the app's own id and
// include flags from the spec table; the grab and delete verbs are identical on
// both apps.
func newQueueCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "queue", Short: "Download queue"}

	root.AddCommand(a.pagedGet("list", "List queue records",
		"/queue", concat(s.QueueList, filterFlags)))
	root.AddCommand(a.queryGet("details", "Queue records for one "+s.Noun,
		"/queue/details", s.QueueDetails))
	root.AddCommand(a.simpleGet("status", "Queue counts and health", "/queue/status"))

	// grab: tell the download client to take a queued release now. Gated: it
	// starts a transfer.
	root.AddCommand(a.postByIDCmd("grab", "Grab one queued release",
		"/queue/grab", "grabbing a queued release", "queue record id"))
	root.AddCommand(a.payloadCmd("grab-bulk", "Grab several queued releases",
		http.MethodPost, "/queue/grab/bulk", "grabbing queued releases"))

	root.AddCommand(a.deleteByIDCmd("delete", "Remove one queue record", "/queue",
		"removing a queue record", "queue record id", queueDeleteFlags))
	root.AddCommand(a.payloadDeleteCmd("delete-bulk", "Remove several queue records",
		"/queue/bulk", "removing queue records", queueDeleteFlags))
	return root
}

// queueDeleteFlags are the removal options both apps share.
var queueDeleteFlags = []qflag{
	{"remove-from-client", "removeFromClient", qBool, "also remove from the download client"},
	{"blocklist", "blocklist", qBool, "blocklist the release"},
	{"skip-redownload", "skipRedownload", qBool, "do not search for a replacement"},
	{"change-category", "changeCategory", qBool, "move to the download client's post-import category"},
}

// newHistoryCmd builds the `history` group. `item` resolves to an app-specific
// path (/history/movie vs /history/series) with an app-specific query key.
func newHistoryCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "history", Short: "Grab/import/failure history"}

	root.AddCommand(a.pagedGet("list", "List history records",
		"/history", concat(s.HistoryList, filterFlags)))
	root.AddCommand(a.historySince(s))
	root.AddCommand(a.historyItem(s))
	root.AddCommand(a.postByIDCmd("mark-failed", "Mark a history record failed",
		"/history/failed", "marking a history record failed", "history record id"))
	return root
}

func (a *App) historySince(s *appSpec) *cobra.Command {
	c := &cobra.Command{
		Use:         "since",
		Short:       "History records since a date",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET " + client.APIPrefix + "/history/since"},
	}
	var date, eventType string
	c.Flags().StringVar(&date, "date", "", "ISO-8601 date/time to search from (required)")
	c.Flags().StringVar(&eventType, "event-type", "", "restrict to one event type")
	b := bindQ(c, s.HistorySince)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if date == "" {
			return output.UsageError("missing required --date (ISO-8601, e.g. 2026-09-01)")
		}
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		q.Set("date", date)
		if eventType != "" {
			q.Set("eventType", eventType)
		}
		res, err := a.get("/history/since", client.Options{Query: q})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

// historyItem wraps the per-resource history endpoint: /history/movie?movieId=
// on Radarr, /history/series?seriesId= on Sonarr.
func (a *App) historyItem(s *appSpec) *cobra.Command {
	c := &cobra.Command{
		Use:   "item",
		Short: "History for one " + s.Noun,
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "GET " + client.APIPrefix + s.HistoryItemPath + "?" + s.HistoryItemQuery + "={id}",
		},
	}
	id := idFlag(c, s.Noun+" id")
	var eventType string
	c.Flags().StringVar(&eventType, "event-type", "", "restrict to one event type")
	b := bindQ(c, s.HistoryItemExtra)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		q.Set(s.HistoryItemQuery, itoa(*id))
		if eventType != "" {
			q.Set("eventType", eventType)
		}
		res, err := a.get(s.HistoryItemPath, client.Options{Query: q})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

func newWantedCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "wanted", Short: "Missing and cutoff-unmet records"}
	root.AddCommand(a.pagedGet("missing", "Monitored records with no file",
		"/wanted/missing", s.WantedMissing))
	root.AddCommand(a.pagedGet("cutoff", "Records below their quality cutoff",
		"/wanted/cutoff", s.WantedCutoff))
	root.AddCommand(a.wantedSearch(s))
	return root
}

// newReleaseCmd builds the `release` group. `search` is a read (an interactive
// indexer query); `grab` starts a download and is deliberately NOT confirm-
// gated — grabbing is the point of the tool. The size-disclosure rule (warn
// before any file over 1 GB) lives in the skill, not the binary.
func newReleaseCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "release", Short: "Interactive indexer search and grab"}
	root.AddCommand(a.queryGet("search", "Search indexers for releases", "/release", s.ReleaseSearch))
	root.AddCommand(a.payloadCmd("grab", "Send one release to the download client",
		http.MethodPost, "/release", ""))
	root.AddCommand(a.payloadCmd("push", "Push an externally-found release",
		http.MethodPost, "/release/push", "pushing a release"))
	return root
}

func newCalendarCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "calendar", Short: "Upcoming and recent releases"}
	root.AddCommand(a.queryGet("list", "Calendar entries in a date range",
		"/calendar", concat(calendarShared, s.CalendarList)))
	return root
}

var calendarShared = []qflag{
	{"start", "start", qString, "range start, ISO-8601"},
	{"end", "end", qString, "range end, ISO-8601"},
	{"unmonitored", "unmonitored", qBool, "include unmonitored records"},
	{"tags", "tags", qString, "comma-separated tag ids"},
}

func newRenameCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "rename", Short: "Preview file renames"}
	root.AddCommand(a.queryGet("preview", "Files whose names differ from the naming scheme",
		"/rename", s.RenamePreview))
	return root
}

// newManualImportCmd builds `manualimport`. `submit` is confirm-gated: it moves
// or hardlinks files into the library.
func newManualImportCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "manualimport", Short: "Manual import candidates"}
	root.AddCommand(a.queryGet("list", "Importable files in a folder or download",
		"/manualimport", concat(manualImportShared, s.ManualImport)))
	root.AddCommand(a.payloadCmd("submit", "Import the selected files",
		http.MethodPost, "/manualimport", "submitting a manual import"))
	return root
}

var manualImportShared = []qflag{
	{"folder", "folder", qString, "folder to scan"},
	{"download-id", "downloadId", qString, "download client id to scan"},
	{"filter-existing", "filterExistingFiles", qBool, "hide files already in the library"},
}

// newParseCmd wraps GET /parse: what the app makes of a release name. Radarr
// takes a title only; Sonarr also takes a path.
func newParseCmd(a *App, s *appSpec) *cobra.Command {
	c := a.queryGet("parse", "Parse a release name the way the app would", "/parse", s.Parse)
	c.Short = "Parse a release name the way " + string(s.Name) + " would"
	return c
}

// postByIDCmd builds `<use> --id N`: POST <prefix>/{id} with no body.
func (a *App) postByIDCmd(use, short, prefix, gateWhat, idUsage string) *cobra.Command {
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "POST " + client.APIPrefix + prefix + "/{id}"},
	}
	id := idFlag(c, idUsage)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if gateWhat != "" {
			if cerr := a.gate(gateWhat); cerr != nil {
				return cerr
			}
		}
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		res, err := a.post(prefix+"/"+itoa(*id), client.Options{})
		if err != nil {
			return err
		}
		return a.emit(res, "ok")
	}
	return c
}

// payloadDeleteCmd builds a DELETE taking a --payload body plus query flags.
func (a *App) payloadDeleteCmd(use, short, path, gateWhat string, extra []qflag) *cobra.Command {
	var payload, payloadFile string
	c := &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "DELETE " + client.APIPrefix + path},
	}
	b := bindQ(c, extra)
	addPayloadFlags(c, &payload, &payloadFile)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := a.gate(gateWhat); cerr != nil {
			return cerr
		}
		body, err := resolvePayload(payload, payloadFile)
		if err != nil {
			return err
		}
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		res, derr := a.do(http.MethodDelete, path, client.Options{Body: body, Query: q})
		if derr != nil {
			return derr
		}
		return a.emit(res, "")
	}
	return c
}
