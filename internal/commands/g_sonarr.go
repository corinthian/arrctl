package commands

import (
	"net/http"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	RegisterFor("series", config.Sonarr, newSeriesCmd)
	RegisterFor("episode", config.Sonarr, newEpisodeCmd)
	RegisterFor("episodefile", config.Sonarr, newEpisodeFileCmd)
	RegisterFor("seasonpass", config.Sonarr, newSeasonPassCmd)
}

// newSeriesCmd builds the Sonarr-only `series` group. The verb set mirrors
// Radarr's `movie` exactly; only the paths and the exclusion query key differ,
// and both come from the app spec.
func newSeriesCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "series", Short: "Shows in the Sonarr library"}

	root.AddCommand(a.queryGet("list", "List series", "/series", s.ResourceListFlags))
	root.AddCommand(a.getByID("get", "Get one series", "/series", "", "series id"))
	root.AddCommand(a.payloadCmd("add", "Add a series to the library",
		http.MethodPost, "/series", "adding a series to the library"))
	root.AddCommand(a.resourceUpdate(s))
	root.AddCommand(a.deleteByIDCmd("delete", "Remove a series from the library", "/series",
		"deleting a series", "series id", deleteFlagsFor(s)))
	root.AddCommand(a.getByID("folder", "The folder Sonarr would use for a series",
		"/series", "/folder", "series id"))
	root.AddCommand(a.payloadCmd("editor", "Bulk-edit series",
		http.MethodPut, "/series/editor", ""))
	root.AddCommand(a.payloadCmd("editor-delete", "Bulk-delete series",
		http.MethodDelete, "/series/editor", "bulk-deleting series"))
	root.AddCommand(a.payloadCmd("import", "Import series from disk",
		http.MethodPost, "/series/import", "importing series"))
	root.AddCommand(a.lookupCmd(s))

	addResourceSugar(root, a, s)
	return root
}

// newEpisodeCmd builds the Sonarr-only `episode` group.
func newEpisodeCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "episode", Short: "Episodes of a series"}
	root.AddCommand(a.episodeList())
	root.AddCommand(a.getByID("get", "Get one episode", "/episode", "", "episode id"))
	root.AddCommand(a.payloadByIDCmd("update", "Update one episode",
		http.MethodPut, "/episode", "", "episode id", nil))
	root.AddCommand(a.episodeMonitor())
	root.AddCommand(a.episodeSearch())
	return root
}

// episodeList requires --series-id: the endpoint refuses an unscoped call.
func (a *App) episodeList() *cobra.Command {
	c := &cobra.Command{
		Use:         "list",
		Short:       "List episodes of one series",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET " + client.APIPrefix + "/episode"},
	}
	b := bindQ(c, []qflag{
		{"series-id", "seriesId", qInt, "series id (required)"},
		{"season", "seasonNumber", qInt, "restrict to one season"},
		{"episode-ids", "episodeIds", qIntSlice, "restrict to these episode ids"},
		{"episode-file-id", "episodeFileId", qInt, "restrict to episodes using one file"},
		{"include-series", "includeSeries", qBool, "embed the series object"},
		{"include-episode-file", "includeEpisodeFile", qBool, "embed the episode file"},
		{"include-images", "includeImages", qBool, "embed image records"},
	})
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if id, ok := b.Int("series-id"); !ok || id <= 0 {
			return output.UsageError("missing required --series-id (a positive integer)")
		}
		q, cerr := b.Values()
		if cerr != nil {
			return cerr
		}
		res, err := a.get("/episode", client.Options{Query: q})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}

// episodeMonitor flips monitoring on a set of episodes. Confirm-gated in both
// directions: turning monitoring ON is what makes an episode a search target,
// so it can lead to a download on the next RSS pass.
func (a *App) episodeMonitor() *cobra.Command {
	var ids []string
	var monitored string
	c := &cobra.Command{
		Use:   "monitor",
		Short: "Set monitoring on a set of episodes",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "PUT " + client.APIPrefix + "/episode/monitor",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cerr := a.gate("changing episode monitoring"); cerr != nil {
				return cerr
			}
			if len(ids) == 0 {
				return output.UsageError("missing required --episode-ids")
			}
			if !cmd.Flags().Changed("monitored") {
				return output.UsageError("missing required --monitored true|false")
			}
			want, cerr := parseBoolFlag("monitored", monitored)
			if cerr != nil {
				return cerr
			}
			nums, cerr := intList(ids, "--episode-ids")
			if cerr != nil {
				return cerr
			}
			body := map[string]interface{}{"episodeIds": nums, "monitored": want}
			res, err := a.put("/episode/monitor", client.Options{Body: body})
			if err != nil {
				return err
			}
			return a.emit(res, "")
		},
	}
	c.Flags().StringSliceVar(&ids, "episode-ids", nil, "episode ids (repeatable/comma-separated)")
	c.Flags().StringVar(&monitored, "monitored", "", "monitoring state to set (true|false)")
	return c
}

// newEpisodeFileCmd builds the Sonarr-only `episodefile` group.
func newEpisodeFileCmd(a *App, s *appSpec) *cobra.Command {
	return a.newFileGroup(fileGroup{
		Use:   "episodefile",
		Short: "Episode files on disk",
		Path:  "/episodefile",
		ListFlags: []qflag{
			{"series-id", "seriesId", qInt, "restrict to one series id"},
			{"episode-file-ids", "episodeFileIds", qIntSlice, "restrict to these episode file ids"},
		},
	})
}

// newSeasonPassCmd wraps POST /seasonpass, which sets monitoring across whole
// seasons. Confirm-gated: monitoring a season makes every episode in it a
// search target.
func newSeasonPassCmd(a *App, s *appSpec) *cobra.Command {
	c := a.payloadCmd("seasonpass", "Set monitoring across seasons",
		http.MethodPost, "/seasonpass", "changing season monitoring")
	c.Annotations["examples"] = `arrctl sonarr seasonpass --confirm --payload ` +
		`'{"series":[{"id":11,"monitored":true}],"monitoringOptions":{"monitor":"none"}}'`
	return c
}
