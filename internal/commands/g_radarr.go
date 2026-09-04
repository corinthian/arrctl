package commands

import (
	"encoding/json"
	"net/http"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	RegisterFor("movie", config.Radarr, newMovieCmd)
	RegisterFor("moviefile", config.Radarr, newMovieFileCmd)
	RegisterFor("collection", config.Radarr, newCollectionCmd)
}

// newMovieCmd builds the Radarr-only `movie` group.
func newMovieCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "movie", Short: "Films in the Radarr library"}

	root.AddCommand(a.queryGet("list", "List films", "/movie", s.ResourceListFlags))
	root.AddCommand(a.getByID("get", "Get one film", "/movie", "", "movie id"))
	root.AddCommand(a.payloadCmd("add", "Add a film to the library",
		http.MethodPost, "/movie", "adding a film to the library"))
	root.AddCommand(a.resourceUpdate(s))
	root.AddCommand(a.deleteByIDCmd("delete", "Remove a film from the library", "/movie",
		"deleting a film", "movie id", deleteFlagsFor(s)))
	root.AddCommand(a.getByID("folder", "The folder Radarr would use for a film",
		"/movie", "/folder", "movie id"))
	root.AddCommand(a.payloadCmd("editor", "Bulk-edit films",
		http.MethodPut, "/movie/editor", ""))
	root.AddCommand(a.payloadCmd("editor-delete", "Bulk-delete films",
		http.MethodDelete, "/movie/editor", "bulk-deleting films"))
	root.AddCommand(a.payloadCmd("import", "Import films from disk",
		http.MethodPost, "/movie/import", "importing films"))
	root.AddCommand(a.lookupCmd(s))

	// Search sugar (Phase 4) lives with the group it belongs to.
	addResourceSugar(root, a, s)
	return root
}

// deleteFlagsFor builds the shared delete options, with the exclusion query key
// taken from the app spec (addImportExclusion vs addImportListExclusion).
func deleteFlagsFor(s *appSpec) []qflag {
	return []qflag{
		{"delete-files", "deleteFiles", qBool, "also delete the files on disk"},
		{"add-exclusion", s.DeleteExclusionQuery, qBool, "add an import-list exclusion so it is not re-added"},
	}
}

// resourceUpdate builds `update --id N --payload`. Servarr's PUT needs the full
// resource back, so the payload is merged over a fresh GET rather than sent
// alone: sending a partial body silently blanks every field it omits.
func (a *App) resourceUpdate(s *appSpec) *cobra.Command {
	var payload, payloadFile string
	c := &cobra.Command{
		Use:   "update",
		Short: "Update one " + s.Noun + " (--payload merges over the current record)",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "PUT " + client.APIPrefix + s.ResourcePath + "/{id}",
			"examples": "arrctl " + string(s.Name) + " " + s.Noun +
				` update --id 37 --payload '{"monitored":false}'`,
		},
	}
	id := idFlag(c, s.Noun+" id")
	moveFiles := c.Flags().String("move-files", "",
		"move existing files when the path changes (true|false)")
	addPayloadFlags(c, &payload, &payloadFile)
	c.RunE = func(cmd *cobra.Command, args []string) error {
		if cerr := requireID(cmd, *id); cerr != nil {
			return cerr
		}
		patch, err := resolvePayload(payload, payloadFile)
		if err != nil {
			return err
		}
		patchMap, ok := patch.(map[string]interface{})
		if !ok {
			return output.UsageError("--payload must be a JSON object for `update`")
		}

		cur, gerr := a.get(s.ResourcePath+"/"+itoa(*id), client.Options{})
		if gerr != nil {
			return gerr
		}
		var base map[string]interface{}
		if uerr := json.Unmarshal(cur.Data, &base); uerr != nil {
			return output.NewError(output.CodeParseError,
				"cannot read the current "+s.Noun+" record to merge over: "+uerr.Error())
		}
		for k, v := range patchMap {
			base[k] = v
		}

		q := map[string][]string{}
		if cmd.Flags().Changed("move-files") {
			mv, berr := parseBoolFlag("move-files", *moveFiles)
			if berr != nil {
				return berr
			}
			q["moveFiles"] = []string{boolStr(mv)}
		}
		res, perr := a.put(s.ResourcePath+"/"+itoa(*id), client.Options{Body: base, Query: q})
		if perr != nil {
			return perr
		}
		return a.emit(res, "")
	}
	return c
}

// lookupCmd builds `lookup --term T` or `lookup --id-type X --id V`. Radarr has
// dedicated /lookup/tmdb and /lookup/imdb paths; Sonarr folds external ids into
// the single /series/lookup?term= endpoint as `term=tvdb:V`.
func (a *App) lookupCmd(s *appSpec) *cobra.Command {
	var term, idType, idVal string
	c := &cobra.Command{
		Use:   "lookup",
		Short: "Search the metadata provider (not the library)",
		Args:  cobra.NoArgs,
		Annotations: map[string]string{
			"endpoint": "GET " + client.APIPrefix + s.ResourcePath + "/lookup",
			"examples": "arrctl " + string(s.Name) + " " + s.Noun + " lookup --term Duel\n" +
				"arrctl " + string(s.Name) + " " + s.Noun + " lookup --id-type " + s.IDTypes[0] + " --id 12345",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			hasTerm := term != ""
			hasID := idType != "" || idVal != ""
			if hasTerm == hasID {
				return output.UsageError("pass exactly one of --term or (--id-type with --id)")
			}
			if hasTerm {
				res, err := a.get(s.ResourcePath+"/lookup",
					client.Options{Query: map[string][]string{"term": {term}}})
				if err != nil {
					return err
				}
				return a.emit(res, "")
			}
			if idType == "" || idVal == "" {
				return output.UsageError("--id-type and --id must be given together")
			}
			if cerr := s.validIDType(idType); cerr != nil {
				return cerr
			}
			path, q := s.lookupByID(idType, idVal)
			res, err := a.get(path, client.Options{Query: q})
			if err != nil {
				return err
			}
			return a.emit(res, "")
		},
	}
	c.Flags().StringVar(&term, "term", "", "free-text search term")
	c.Flags().StringVar(&idType, "id-type", "", "external id space: "+joinTypes(s))
	c.Flags().StringVar(&idVal, "id", "", "external id value")
	return c
}

// lookupByID returns the path and query for an external-id lookup.
func (s *appSpec) lookupByID(idType, val string) (string, map[string][]string) {
	if s.Name == config.Radarr {
		// Radarr serves each id space on its own path with its own query key.
		key := map[string]string{"tmdb": "tmdbId", "imdb": "imdbId"}[idType]
		return s.ResourcePath + "/lookup/" + idType, map[string][]string{key: {val}}
	}
	// Sonarr has one lookup path; external ids ride in the term as `tvdb:123`.
	return s.ResourcePath + "/lookup", map[string][]string{"term": {idType + ":" + val}}
}

func joinTypes(s *appSpec) string {
	out := ""
	for i, t := range s.IDTypes {
		if i > 0 {
			out += "|"
		}
		out += t
	}
	return out
}

// newMovieFileCmd builds the Radarr-only `moviefile` group.
func newMovieFileCmd(a *App, s *appSpec) *cobra.Command {
	return a.newFileGroup(fileGroup{
		Use:   "moviefile",
		Short: "Film files on disk",
		Path:  "/moviefile",
		ListFlags: []qflag{
			{"movie-id", "movieId", qIntSlice, "restrict to these movie ids"},
			{"movie-file-ids", "movieFileIds", qIntSlice, "restrict to these movie file ids"},
		},
	})
}

// fileGroup describes the moviefile/episodefile groups, identical but for the
// noun and the list-time query keys.
type fileGroup struct {
	Use       string
	Short     string
	Path      string
	ListFlags []qflag
}

func (a *App) newFileGroup(g fileGroup) *cobra.Command {
	root := &cobra.Command{Use: g.Use, Short: g.Short}
	root.AddCommand(a.queryGet("list", "List "+g.Use+" records", g.Path, g.ListFlags))
	root.AddCommand(a.getByID("get", "Get one "+g.Use+" record", g.Path, "", g.Use+" id"))
	root.AddCommand(a.payloadByIDCmd("update", "Update one "+g.Use+" record",
		http.MethodPut, g.Path, "", g.Use+" id", nil))
	root.AddCommand(a.deleteByIDCmd("delete", "Delete one file from disk", g.Path,
		"deleting a "+g.Use+" from disk", g.Use+" id", nil))
	root.AddCommand(a.payloadCmd("bulk-update", "Update several "+g.Use+" records",
		http.MethodPut, g.Path+"/bulk", ""))
	root.AddCommand(a.payloadCmd("bulk-delete", "Delete several files from disk",
		http.MethodDelete, g.Path+"/bulk", "bulk-deleting "+g.Use+" records from disk"))
	root.AddCommand(a.payloadCmd("editor", "Bulk-edit "+g.Use+" records",
		http.MethodPut, g.Path+"/editor", ""))
	return root
}

// newCollectionCmd builds the Radarr-only `collection` group.
func newCollectionCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "collection", Short: "TMDb collections"}
	root.AddCommand(a.queryGet("list", "List collections", "/collection", []qflag{
		{"tmdb-id", "tmdbId", qInt, "filter to one collection's TMDb id"},
	}))
	root.AddCommand(a.getByID("get", "Get one collection", "/collection", "", "collection id"))
	root.AddCommand(a.payloadByIDCmd("update", "Update one collection",
		http.MethodPut, "/collection", "", "collection id", nil))
	root.AddCommand(a.payloadCmd("update-bulk", "Update several collections",
		http.MethodPut, "/collection", ""))
	return root
}
