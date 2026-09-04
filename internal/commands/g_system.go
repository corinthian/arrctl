package commands

import (
	"github.com/corinthian/arrctl/internal/client"
	"github.com/spf13/cobra"
)

func init() {
	Register("system", newSystemCmd)
	Register("health", newHealthCmd)
	Register("diskspace", newDiskspaceCmd)
	Register("ping", newPingCmd)
	Register("api-info", newAPIInfoCmd)
	Register("filesystem", newFilesystemCmd)
	Register("rootfolder", newRootFolderCmd)
	Register("qualityprofile", newQualityProfileCmd)
	Register("tag", newTagCmd)
}

// newSystemCmd builds the `system` group: status, task, and the two gated
// lifecycle verbs. restart and shutdown are confirm-gated and are never run as
// part of any test.
func newSystemCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "system", Short: "Instance status, tasks and lifecycle"}
	root.AddCommand(a.simpleGet("status", "Instance version and paths", "/system/status"))

	task := &cobra.Command{Use: "task", Short: "Scheduled tasks"}
	task.AddCommand(a.simpleGet("list", "List scheduled tasks", "/system/task"))
	task.AddCommand(a.getByID("get", "Get one scheduled task", "/system/task", "", "task id"))
	root.AddCommand(task)

	root.AddCommand(a.gatedPost("restart", "Restart the instance", "/system/restart",
		"restarting "+string(s.Name)))
	root.AddCommand(a.gatedPost("shutdown", "Shut the instance down", "/system/shutdown",
		"shutting "+string(s.Name)+" down"))
	return root
}

// gatedPost builds a bodyless POST behind a confirm gate.
func (a *App) gatedPost(use, short, path, what string) *cobra.Command {
	return &cobra.Command{
		Use:         use,
		Short:       short,
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "POST " + client.APIPrefix + path},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cerr := a.gate(what); cerr != nil {
				return cerr
			}
			res, err := a.post(path, client.Options{})
			if err != nil {
				return err
			}
			return a.emit(res, "ok")
		},
	}
}

func newHealthCmd(a *App, s *appSpec) *cobra.Command {
	c := a.simpleGet("health", "Instance health checks (empty is clean)", "/health")
	return c
}

func newDiskspaceCmd(a *App, s *appSpec) *cobra.Command {
	return a.simpleGet("diskspace", "Free space per mounted root", "/diskspace")
}

// newPingCmd wraps GET /ping, which sits outside /api/v3 and needs no key.
func newPingCmd(a *App, s *appSpec) *cobra.Command {
	return &cobra.Command{
		Use:         "ping",
		Short:       "Liveness check (no API key required)",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET /ping"},
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.get("/ping", client.Options{NoPrefix: true, NoKey: true})
			if err != nil {
				return err
			}
			return a.emit(res, "pong")
		},
	}
}

// newAPIInfoCmd wraps GET /api, which sits outside the /api/v3 prefix but,
// unlike /ping, DOES require the key: both live instances answer it 401 without
// one (verified 2026-09-04), contrary to the spec's "needs no key" note.
func newAPIInfoCmd(a *App, s *appSpec) *cobra.Command {
	return &cobra.Command{
		Use:         "api-info",
		Short:       "API version metadata",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET /api"},
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := a.get("/api", client.Options{NoPrefix: true})
			if err != nil {
				return err
			}
			return a.emit(res, "")
		},
	}
}

// newFilesystemCmd wraps GET /filesystem. --include-files defaults to true:
// the bare listing hides files and misled a diagnosis on 2026-09-04.
func newFilesystemCmd(a *App, s *appSpec) *cobra.Command {
	var path, includeFiles string
	c := &cobra.Command{
		Use:         "filesystem",
		Short:       "Browse the instance's filesystem (files shown by default)",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET " + client.APIPrefix + "/filesystem"},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := client.Options{Query: map[string][]string{}}
			if path != "" {
				opts.Query.Set("path", path)
			}
			inc, cerr := parseBoolFlag("include-files", includeFiles)
			if cerr != nil {
				return cerr
			}
			opts.Query.Set("includeFiles", boolStr(inc))
			res, err := a.get("/filesystem", opts)
			if err != nil {
				return err
			}
			return a.emit(res, "")
		},
	}
	c.Flags().StringVar(&path, "path", "", "directory to list")
	// On by default: the bare listing hides files and misled a diagnosis on
	// 2026-09-04.
	c.Flags().StringVar(&includeFiles, "include-files", "true",
		"include files, not just directories (true|false)")
	return c
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func newRootFolderCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "rootfolder", Short: "Library root folders"}
	root.AddCommand(a.simpleGet("list", "List root folders", "/rootfolder"))
	root.AddCommand(a.getByID("get", "Get one root folder", "/rootfolder", "", "root folder id"))
	root.AddCommand(a.payloadCmd("add", "Add a root folder", "POST", "/rootfolder", ""))
	root.AddCommand(a.deleteByIDCmd("delete", "Delete a root folder", "/rootfolder",
		"deleting a root folder", "root folder id", nil))
	return root
}

func newQualityProfileCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "qualityprofile", Short: "Quality profiles"}
	root.AddCommand(a.simpleGet("list", "List quality profiles", "/qualityprofile"))
	root.AddCommand(a.getByID("get", "Get one quality profile", "/qualityprofile", "", "profile id"))
	root.AddCommand(a.simpleGet("schema", "Blank profile schema for `add`", "/qualityprofile/schema"))
	root.AddCommand(a.payloadCmd("add", "Add a quality profile", "POST", "/qualityprofile", ""))
	root.AddCommand(a.payloadByIDCmd("update", "Replace a quality profile", "PUT",
		"/qualityprofile", "", "profile id", nil))
	root.AddCommand(a.deleteByIDCmd("delete", "Delete a quality profile", "/qualityprofile",
		"deleting a quality profile", "profile id", nil))
	return root
}

func newTagCmd(a *App, s *appSpec) *cobra.Command {
	root := &cobra.Command{Use: "tag", Short: "Library tags"}
	root.AddCommand(a.simpleGet("list", "List tags", "/tag"))
	root.AddCommand(a.getByID("get", "Get one tag", "/tag", "", "tag id"))
	root.AddCommand(a.tagDetail())
	root.AddCommand(a.payloadCmd("add", "Add a tag", "POST", "/tag", ""))
	root.AddCommand(a.payloadByIDCmd("update", "Rename a tag", "PUT", "/tag", "", "tag id", nil))
	root.AddCommand(a.deleteByIDCmd("delete", "Delete a tag", "/tag",
		"deleting a tag", "tag id", nil))
	return root
}

// tagDetail wraps /tag/detail with an optional --id selecting one tag.
func (a *App) tagDetail() *cobra.Command {
	c := &cobra.Command{
		Use:         "detail",
		Short:       "Tags with the records that use them",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{"endpoint": "GET " + client.APIPrefix + "/tag/detail[/{id}]"},
	}
	id := idFlag(c, "tag id (omit for all tags)")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		path := "/tag/detail"
		if cmd.Flags().Changed("id") {
			if cerr := requireID(cmd, *id); cerr != nil {
				return cerr
			}
			path += "/" + itoa(*id)
		}
		res, err := a.get(path, client.Options{})
		if err != nil {
			return err
		}
		return a.emit(res, "")
	}
	return c
}
