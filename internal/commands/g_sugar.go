package commands

import (
	"strconv"
	"strings"
	"time"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

func init() { Register("rss", newRSSCmd) }

// The search sugar is a set of pure aliases over `command run`: each is one
// POST to /api/v3/command with the body shown in its --llm help, then returns
// the command record. None ranks releases and none grabs on its own — the app's
// own automatic search does the grabbing, under its own profile rules.
//
// Every sugar verb accepts --wait to chain `command wait`.

// sugarBody builds the {name, ...} body for a sugar verb.
type sugarBody map[string]interface{}

// dispatch posts a command body and optionally waits for it. The wait
// configuration is validated first, before anything is dispatched: a command
// posted under a wait that cannot poll is one running with nothing watching it.
func (a *App) dispatch(body sugarBody, wait bool, timeout, interval time.Duration) error {
	if cerr := validateWait(timeout, interval, "--wait-timeout", "--wait-interval"); cerr != nil {
		return cerr
	}
	res, err := a.post("/command", client.Options{Body: body})
	if err != nil {
		return err
	}
	if !wait {
		return a.emit(res, terseCommand(res.Data))
	}
	return a.waitAndEmit(res.Data, timeout, interval)
}

// intList parses a repeated/comma-separated flag into integers.
func intList(vals []string, flag string) ([]int, *output.CLIError) {
	out := []int{}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, output.UsageError(flag + " takes integers; got " + strconv.Quote(v))
		}
		if n <= 0 {
			return nil, output.UsageError(flag + " takes positive integers; got " + strconv.Quote(v))
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, output.UsageError("missing required " + flag)
	}
	return out, nil
}

// addResourceSugar hangs search/refresh/rescan off the movie/series group.
func addResourceSugar(root *cobra.Command, a *App, s *appSpec) {
	root.AddCommand(a.sugarIDs("search",
		"Ask "+string(s.Name)+" to search for this "+s.Noun,
		s, s.SearchCommand))
	root.AddCommand(a.sugarIDs("refresh",
		"Refresh metadata for this "+s.Noun, s, s.RefreshCommand))
	root.AddCommand(a.sugarIDs("rescan",
		"Rescan this "+s.Noun+"'s folder on disk", s, s.RescanCommand))
}

// sugarIDs builds a sugar verb taking --id (repeatable where the command name
// takes a plural id field). The body shape per command name is fixed by the
// API, so it is chosen here rather than guessed by the caller — that payload
// shape is the one thing an LLM caller reliably gets wrong.
func (a *App) sugarIDs(use, short string, s *appSpec, name string) *cobra.Command {
	var ids []string
	var wait bool
	var timeout, interval time.Duration
	c := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "POST " + client.APIPrefix + "/command {\"name\":\"" + name + "\"}",
			"examples": "arrctl " + string(s.Name) + " " + s.Noun + " " + use + " --id 37\n" +
				"arrctl " + string(s.Name) + " " + s.Noun + " " + use + " --id 37 --wait",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			nums, cerr := intList(ids, "--id")
			if cerr != nil {
				return cerr
			}
			body, cerr := commandBodyFor(name, nums)
			if cerr != nil {
				return cerr
			}
			return a.dispatch(body, wait, timeout, interval)
		},
	}
	c.Flags().StringSliceVar(&ids, "id", nil, s.Noun+" id (repeatable/comma-separated)")
	addWaitFlags(c, &wait, &timeout, &interval)
	return c
}

// commandBodyFor returns the documented body for a command name. The plural vs
// singular id field is a per-command fact from the two APIs, verified live
// 2026-09-04 for MoviesSearch, RescanMovie and RefreshMovie.
//
// A singular shape takes exactly one id and is an error on more than one, never
// a body built from the first with the rest dropped. Fanning out would mean
// several POSTs and several command records, but the envelope is single-record
// shaped: `data` is one record, --terse is one line, --wait polls one id. That
// is a multi-result contract this tool does not have and the spec does not
// describe, so the caller loops instead.
func commandBodyFor(name string, ids []int) (sugarBody, *output.CLIError) {
	single := func(field string) (sugarBody, *output.CLIError) {
		if len(ids) > 1 {
			return nil, output.UsageErrorHint(
				name+" takes exactly one id; got "+itoa(len(ids)),
				"this command name carries a single "+field+
					", so run it once per id rather than passing a list")
		}
		return sugarBody{"name": name, field: ids[0]}, nil
	}
	switch name {
	case "MoviesSearch":
		return sugarBody{"name": name, "movieIds": ids}, nil
	case "RefreshMovie":
		return sugarBody{"name": name, "movieIds": ids}, nil
	case "RescanMovie":
		return single("movieId")
	case "SeriesSearch":
		return single("seriesId")
	case "RefreshSeries":
		return single("seriesId")
	case "RescanSeries":
		return single("seriesId")
	}
	return nil, output.NewError(output.CodeParseError,
		"no known body shape for command "+name)
}

// wantedSearch is `<app> wanted search`: MissingMoviesSearch on Radarr,
// MissingEpisodeSearch on Sonarr. It searches for EVERY monitored missing
// record and will start downloads, so it is confirm-gated even though its name
// is on the `command run` allowlist.
func (a *App) wantedSearch(s *appSpec) *cobra.Command {
	var wait bool
	var timeout, interval time.Duration
	c := &cobra.Command{
		Use:   "search",
		Short: "Search for every monitored missing record (starts downloads)",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "POST " + client.APIPrefix + "/command {\"name\":\"" + s.MissingSearch + "\"}",
			"examples": "arrctl " + string(s.Name) + " wanted search --confirm",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cerr := a.gate("a library-wide missing search on " + string(s.Name) +
				", which grabs every monitored missing record it finds"); cerr != nil {
				return cerr
			}
			return a.dispatch(sugarBody{"name": s.MissingSearch}, wait, timeout, interval)
		},
	}
	addWaitFlags(c, &wait, &timeout, &interval)
	return c
}

// seasonSearch and episodeSearch are Sonarr-only sugar.
func (a *App) seasonSearch() *cobra.Command {
	var wait bool
	var timeout, interval time.Duration
	c := &cobra.Command{
		Use:   "search",
		Short: "Search for one season of a series",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "POST " + client.APIPrefix + "/command {\"name\":\"SeasonSearch\"}",
			"examples": "arrctl sonarr season search --id 11 --season 1",
		},
	}
	id := idFlag(c, "series id")
	season := c.Flags().Int("season", -1, "season number")
	addWaitFlags(c, &wait, &timeout, &interval)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		if !cmd.Flags().Changed("season") {
			return output.UsageError("missing required --season")
		}
		return a.dispatch(sugarBody{
			"name": "SeasonSearch", "seriesId": *id, "seasonNumber": *season,
		}, wait, timeout, interval)
	}
	return c
}

func (a *App) episodeSearch() *cobra.Command {
	var ids []string
	var wait bool
	var timeout, interval time.Duration
	c := &cobra.Command{
		Use:   "search",
		Short: "Search for specific episodes",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "POST " + client.APIPrefix + "/command {\"name\":\"EpisodeSearch\"}",
			"examples": "arrctl sonarr episode search --episode-ids 101,102",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			nums, cerr := intList(ids, "--episode-ids")
			if cerr != nil {
				return cerr
			}
			return a.dispatch(sugarBody{"name": "EpisodeSearch", "episodeIds": nums},
				wait, timeout, interval)
		},
	}
	c.Flags().StringSliceVar(&ids, "episode-ids", nil, "episode ids (repeatable/comma-separated)")
	addWaitFlags(c, &wait, &timeout, &interval)
	return c
}

// newRSSCmd is `<app> rss sync`: ask the app to poll its indexers' RSS feeds
// now. Confirm-gated: an RSS pass grabs anything it finds that the library
// wants.
func newRSSCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "rss", Short: "Indexer RSS polling"}
	var wait bool
	var timeout, interval time.Duration
	c := &cobra.Command{
		Use:   "sync",
		Short: "Poll indexer RSS feeds now (may grab releases)",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "POST " + client.APIPrefix + "/command {\"name\":\"RssSync\"}",
			"examples": "arrctl " + string(s.Name) + " rss sync --confirm",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cerr := a.gate("an RSS sync on " + string(s.Name) +
				", which grabs any wanted release the feeds are offering"); cerr != nil {
				return cerr
			}
			return a.dispatch(sugarBody{"name": "RssSync"}, wait, timeout, interval)
		},
	}
	addWaitFlags(c, &wait, &timeout, &interval)
	root.AddCommand(c)
	return root
}

// seasonGroup is the Sonarr-only `season` group holding season search.
func newSeasonCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "season", Short: "Whole-season operations"}
	root.AddCommand(a.seasonSearch())
	return root
}

func init() { RegisterFor("season", config.Sonarr, newSeasonCmd) }
