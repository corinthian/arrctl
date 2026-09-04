package commands

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

// qkind is a query flag's value type.
type qkind int

const (
	qString qkind = iota
	qBool
	qInt
	qIntSlice
	qStringSlice
)

// qflag binds one CLI flag to one query parameter. The spec's "shared groups
// with app-specific flags" table is expressed entirely as slices of these, so
// the difference between Radarr and Sonarr is data, not code.
type qflag struct {
	Name  string // CLI flag name, without the leading --
	Query string // API query parameter
	Kind  qkind
	Usage string
}

// qbind holds the parsed values for a set of qflags on one command.
type qbind struct {
	cmd    *cobra.Command
	flags  []qflag
	strs   map[string]*string
	bools  map[string]*bool
	ints   map[string]*int
	slices map[string]*[]string
}

// bindQ registers every qflag on cmd and returns a binding whose Values()
// yields only the flags the caller actually set.
func bindQ(cmd *cobra.Command, flags []qflag) *qbind {
	b := &qbind{
		cmd: cmd, flags: flags,
		strs:   map[string]*string{},
		bools:  map[string]*bool{},
		ints:   map[string]*int{},
		slices: map[string]*[]string{},
	}
	for _, f := range flags {
		switch f.Kind {
		case qBool:
			// A value-taking boolean, not a bare toggle: `--monitored true`,
			// which is how the spec and the acceptance table write it, and how
			// an LLM caller reliably writes it. A bare `--monitored` would
			// otherwise leave "true" as a stray positional argument and fail.
			p := new(string)
			cmd.Flags().StringVar(p, f.Name, "", f.Usage+" (true|false)")
			b.strs[f.Name] = p
		case qInt:
			p := new(int)
			cmd.Flags().IntVar(p, f.Name, 0, f.Usage)
			b.ints[f.Name] = p
		case qIntSlice, qStringSlice:
			p := new([]string)
			cmd.Flags().StringSliceVar(p, f.Name, nil, f.Usage)
			b.slices[f.Name] = p
		default:
			p := new(string)
			cmd.Flags().StringVar(p, f.Name, "", f.Usage)
			b.strs[f.Name] = p
		}
	}
	return b
}

// Values builds the query string from the flags the caller changed. An untouched
// flag is omitted entirely so the instance's own default applies.
func (b *qbind) Values() (url.Values, *output.CLIError) {
	q := url.Values{}
	if b == nil {
		return q, nil
	}
	for _, f := range b.flags {
		if !b.cmd.Flags().Changed(f.Name) {
			continue
		}
		switch f.Kind {
		case qBool:
			v, cerr := parseBoolFlag(f.Name, *b.strs[f.Name])
			if cerr != nil {
				return nil, cerr
			}
			q.Set(f.Query, strconv.FormatBool(v))
		case qInt:
			q.Set(f.Query, strconv.Itoa(*b.ints[f.Name]))
		case qIntSlice:
			for _, v := range *b.slices[f.Name] {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				if _, err := strconv.Atoi(v); err != nil {
					return nil, output.UsageError("--" + f.Name + " takes integers; got " + strconv.Quote(v))
				}
				q.Add(f.Query, v)
			}
		case qStringSlice:
			for _, v := range *b.slices[f.Name] {
				if v = strings.TrimSpace(v); v != "" {
					q.Add(f.Query, v)
				}
			}
		default:
			q.Set(f.Query, *b.strs[f.Name])
		}
	}
	return q, nil
}

// parseBoolFlag reads a value-taking boolean flag strictly. Anything other than
// true/false is a usage error rather than a silent false.
func parseBoolFlag(name, raw string) (bool, *output.CLIError) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	}
	return false, output.UsageErrorHint(
		"--"+name+" takes true or false; got "+strconv.Quote(raw),
		"write it with a value, e.g. --"+name+" true")
}

// Int returns a bound --flag's integer value and whether it was set.
func (b *qbind) Int(name string) (int, bool) {
	p, ok := b.ints[name]
	if !ok {
		return 0, false
	}
	return *p, b.cmd.Flags().Changed(name)
}

// appSpec is the per-app difference table. Every field is either a flag set for
// one command or a path/query name that differs between the two APIs. Adding an
// app-specific flag means adding a row here, never a branch in a group file.
type appSpec struct {
	Name config.App
	Noun string // "movie" / "series", used in help text

	// Per-command app-specific query flags (the spec's second shared table).
	QueueList     []qflag
	QueueDetails  []qflag
	HistoryList   []qflag
	HistorySince  []qflag
	WantedMissing []qflag
	WantedCutoff  []qflag
	ReleaseSearch []qflag
	CalendarList  []qflag
	RenamePreview []qflag
	ManualImport  []qflag
	Parse         []qflag

	// history item --id N resolves to an app-specific path + query key.
	HistoryItemPath  string
	HistoryItemQuery string

	// Extra query flags on `history item` beyond the id (Sonarr takes a season).
	HistoryItemExtra []qflag

	// Resource-noun group settings (Phase 3).
	ResourcePath         string   // "/movie" / "/series"
	ResourceListFlags    []qflag  // list-time query flags
	DeleteExclusionQuery string   // addImportExclusion / addImportListExclusion
	IDTypes              []string // accepted --id-type values

	// Command names for the search sugar.
	SearchCommand  string // MoviesSearch / SeriesSearch
	RefreshCommand string // RefreshMovie / RefreshSeries
	RescanCommand  string // RescanMovie / RescanSeries
	MissingSearch  string // MissingMoviesSearch / MissingEpisodeSearch
}

// pagingFlags are the paged-list query flags shared by both apps. --all is a
// root persistent flag, applied by the client.
var pagingFlags = []qflag{
	{"page", "page", qInt, "page number"},
	{"page-size", "pageSize", qInt, "records per page"},
	{"sort-key", "sortKey", qString, "sort key"},
	{"sort-dir", "sortDirection", qString, "sort direction: default|ascending|descending"},
}

// filterFlags are the queue/history filters both apps accept identically.
var filterFlags = []qflag{
	{"protocol", "protocol", qString, "download protocol: usenet|torrent"},
	{"languages", "languages", qIntSlice, "language ids (repeatable/comma-separated)"},
	{"quality", "quality", qIntSlice, "quality ids (repeatable/comma-separated)"},
	{"status", "status", qStringSlice, "queue status values"},
	{"event-type", "eventType", qStringSlice, "history event types"},
	{"download-id", "downloadId", qString, "download client id"},
}

func concat(sets ...[]qflag) []qflag {
	out := []qflag{}
	for _, s := range sets {
		out = append(out, s...)
	}
	return out
}

// radarrSpec is the Radarr flag/query table, taken from
// src/Radarr.Api.V3/openapi.json (develop, fetched 2026-09-04).
var radarrSpec = &appSpec{
	Name: config.Radarr,
	Noun: "movie",

	QueueList: []qflag{
		{"movie-ids", "movieIds", qIntSlice, "restrict to these movie ids"},
		{"include-movie", "includeMovie", qBool, "embed the movie object"},
		{"include-unknown-movie-items", "includeUnknownMovieItems", qBool, "include items with no matching movie"},
	},
	QueueDetails: []qflag{
		{"movie-id", "movieId", qInt, "movie id"},
		{"include-movie", "includeMovie", qBool, "embed the movie object"},
	},
	HistoryList: []qflag{
		{"movie-ids", "movieIds", qIntSlice, "restrict to these movie ids"},
		{"include-movie", "includeMovie", qBool, "embed the movie object"},
	},
	HistorySince: []qflag{
		{"include-movie", "includeMovie", qBool, "embed the movie object"},
	},
	WantedMissing: []qflag{
		{"monitored", "monitored", qBool, "only monitored items"},
	},
	WantedCutoff: []qflag{
		{"monitored", "monitored", qBool, "only monitored items"},
	},
	ReleaseSearch: []qflag{
		{"movie-id", "movieId", qInt, "movie id to search releases for"},
	},
	CalendarList: []qflag{},
	RenamePreview: []qflag{
		{"movie-id", "movieId", qIntSlice, "movie ids to preview renames for"},
	},
	ManualImport: []qflag{
		{"movie-id", "movieId", qInt, "movie id"},
	},
	Parse: []qflag{
		{"title", "title", qString, "release title to parse"},
	},

	HistoryItemPath:  "/history/movie",
	HistoryItemQuery: "movieId",
	HistoryItemExtra: []qflag{
		{"include-movie", "includeMovie", qBool, "embed the movie object"},
	},

	ResourcePath: "/movie",
	ResourceListFlags: []qflag{
		{"tmdb-id", "tmdbId", qInt, "filter to a single TMDb id"},
		{"exclude-local-covers", "excludeLocalCovers", qBool, "omit local cover paths"},
	},
	DeleteExclusionQuery: "addImportExclusion",
	IDTypes:              []string{"tmdb", "imdb"},

	SearchCommand:  "MoviesSearch",
	RefreshCommand: "RefreshMovie",
	RescanCommand:  "RescanMovie",
	MissingSearch:  "MissingMoviesSearch",
}

// sonarrSpec is the Sonarr flag/query table, taken from
// src/Sonarr.Api.V3/openapi.json (develop, fetched 2026-09-04).
var sonarrSpec = &appSpec{
	Name: config.Sonarr,
	Noun: "series",

	QueueList: []qflag{
		{"series-ids", "seriesIds", qIntSlice, "restrict to these series ids"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode", "includeEpisode", qBool, "embed the episode object"},
		{"include-unknown-series-items", "includeUnknownSeriesItems", qBool, "include items with no matching series"},
	},
	QueueDetails: []qflag{
		{"series-id", "seriesId", qInt, "series id"},
		{"episode-ids", "episodeIds", qIntSlice, "episode ids"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode", "includeEpisode", qBool, "embed the episode object"},
	},
	HistoryList: []qflag{
		{"series-ids", "seriesIds", qIntSlice, "restrict to these series ids"},
		{"episode-id", "episodeId", qInt, "restrict to one episode id"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode", "includeEpisode", qBool, "embed the episode object"},
	},
	HistorySince: []qflag{
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode", "includeEpisode", qBool, "embed the episode object"},
	},
	WantedMissing: []qflag{
		{"monitored", "monitored", qBool, "only monitored items"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-images", "includeImages", qBool, "embed image records"},
	},
	WantedCutoff: []qflag{
		{"monitored", "monitored", qBool, "only monitored items"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-images", "includeImages", qBool, "embed image records"},
		{"include-episode-file", "includeEpisodeFile", qBool, "embed the episode file"},
	},
	ReleaseSearch: []qflag{
		{"series-id", "seriesId", qInt, "series id to search releases for"},
		{"episode-id", "episodeId", qInt, "episode id to search releases for"},
		{"season", "seasonNumber", qInt, "season number"},
	},
	CalendarList: []qflag{
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode-file", "includeEpisodeFile", qBool, "embed the episode file"},
		{"include-episode-images", "includeEpisodeImages", qBool, "embed episode images"},
	},
	RenamePreview: []qflag{
		{"series-id", "seriesId", qInt, "series id to preview renames for"},
		{"season", "seasonNumber", qInt, "season number"},
	},
	ManualImport: []qflag{
		{"series-id", "seriesId", qInt, "series id"},
		{"season", "seasonNumber", qInt, "season number"},
	},
	Parse: []qflag{
		{"title", "title", qString, "release title to parse"},
		{"path", "path", qString, "file path to parse"},
	},

	HistoryItemPath:  "/history/series",
	HistoryItemQuery: "seriesId",
	HistoryItemExtra: []qflag{
		{"season", "seasonNumber", qInt, "season number"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode", "includeEpisode", qBool, "embed the episode object"},
	},

	ResourcePath: "/series",
	ResourceListFlags: []qflag{
		{"tvdb-id", "tvdbId", qInt, "filter to a single TVDb id"},
		{"include-season-images", "includeSeasonImages", qBool, "embed season images"},
	},
	DeleteExclusionQuery: "addImportListExclusion",
	IDTypes:              []string{"tvdb", "imdb", "tmdb"},

	SearchCommand:  "SeriesSearch",
	RefreshCommand: "RefreshSeries",
	RescanCommand:  "RescanSeries",
	MissingSearch:  "MissingEpisodeSearch",
}

// specs maps each app to its table.
var specs = map[config.App]*appSpec{
	config.Radarr: radarrSpec,
	config.Sonarr: sonarrSpec,
}

// validIDType checks --id-type against the app's accepted set.
func (s *appSpec) validIDType(v string) *output.CLIError {
	for _, t := range s.IDTypes {
		if t == v {
			return nil
		}
	}
	return output.UsageErrorHint(
		"invalid --id-type "+strconv.Quote(v)+" for "+string(s.Name),
		string(s.Name)+" accepts --id-type "+strings.Join(s.IDTypes, "|")+
			"; resolve any other id space before calling arrctl")
}
