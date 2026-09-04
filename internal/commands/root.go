package commands

import (
	"errors"
	"os"
	"strings"

	"github.com/corinthian/arrctl/internal/client"
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

// NewRoot builds the root command with the two app sub-roots, binds global
// flags, registers every group, and wires the --llm short-circuit. Returns the
// root and the App so main can emit typed errors with the right exit code.
func NewRoot() (*cobra.Command, *App) {
	app := NewApp()
	g := app.Flags

	root := &cobra.Command{
		Use:   "arrctl",
		Short: "JSON-first CLI over the Radarr and Sonarr v3 APIs",
		Long: "arrctl <app> <group> <verb> [flags]\n\n" +
			"<app> is radarr or sonarr, always the first positional and always required.\n" +
			"There is no default app: a stale default would silently target the wrong instance.",
		Version:       Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if g.LLM {
				emitLLMHelp(cmd, app.Out)
				os.Exit(0)
			}
			app.Out.Format = app.resolveFormat()
			app.insecureSet = cmd.Flags().Changed("insecure")

			if !client.ValidKeyPlacement(g.APIKeyIn) {
				return output.UsageErrorHint(
					"invalid --apikey-in "+g.APIKeyIn, "valid values: header, query")
			}
			if g.SortDir != "" && !validSortDir(g.SortDir) {
				return output.UsageErrorHint(
					"invalid --sort-dir "+g.SortDir, "valid values: default, ascending, descending")
			}

			loaded, err := config.LoadFile(g.ConfigPath)
			if err != nil {
				return output.NewError(output.CodeBadConfig, err.Error())
			}
			app.Loaded = loaded

			// Record the target app; the client itself is built lazily on the
			// first request, so a usage error is never masked by a config one.
			if target, ok := targetApp(cmd); ok {
				app.setTarget(target)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// A bare `arrctl` prints help; `arrctl <anything unknown>` is
			// handled by cobra's unknown-command error, typed below.
			if err := cmd.Help(); err != nil {
				return output.NewError(output.CodeParseError, "writing help: "+err.Error())
			}
			return nil
		},
	}

	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return output.UsageErrorHint(err.Error(),
			"run `"+c.CommandPath()+" --help` for flags, or `--llm` for JSON help")
	})

	pf := root.PersistentFlags()
	pf.StringVar(&g.URL, "url", "", "instance base URL (overrides env/config)")
	pf.StringVar(&g.APIKey, "apikey", "", "API key (overrides env/config/Keychain)")
	pf.BoolVar(&g.Insecure, "insecure", false, "skip TLS verification for this app")
	pf.StringVar(&g.Timeout, "timeout", "", "HTTP timeout, e.g. 30s (default 30s)")
	pf.StringVar(&g.ConfigPath, "config", "", "path to config.toml")
	pf.StringVar(&g.APIKeyIn, "apikey-in", "header", "where to place the API key: header|query")

	pf.IntVar(&g.Page, "page", 0, "page number (paged lists)")
	pf.IntVar(&g.PageSize, "page-size", 0, "records per page (paged lists)")
	pf.StringVar(&g.SortKey, "sort-key", "", "sort key (paged lists)")
	pf.StringVar(&g.SortDir, "sort-dir", "", "sort direction: default|ascending|descending")
	pf.BoolVar(&g.All, "all", false, "auto-paginate a paged list (capped at 100 pages)")

	pf.BoolVar(&g.Confirm, "confirm", false, "authorize a confirm-gated action")
	pf.BoolVar(&g.LLM, "llm", false, "emit machine-readable JSON help and exit")

	pf.BoolVar(&g.Raw, "raw", false, "pass the app's response through untouched")
	pf.BoolVar(&g.NDJSON, "ndjson", false, "emit one JSON object per line (lists)")
	pf.BoolVar(&g.Terse, "terse", false, "emit a one-line human summary")

	for _, a := range config.Apps {
		root.AddCommand(newAppRoot(app, specs[a]))
	}
	root.AddCommand(newCommandsCmd(app))
	root.AddCommand(newConfigCmd(app))
	root.AddCommand(newVersionCmd(app))

	root.SetHelpTemplate(root.HelpTemplate() +
		"\nFor machine-readable JSON help, append --llm to any command.\n")
	return root, app
}

// newAppRoot builds the `radarr` / `sonarr` sub-root and hangs every group
// registered for that app off it.
func newAppRoot(app *App, spec *appSpec) *cobra.Command {
	c := &cobra.Command{
		Use:   string(spec.Name),
		Short: "Target the " + string(spec.Name) + " instance",
		Annotations: map[string]string{
			"app": string(spec.Name),
		},
	}
	addAll(c, app, spec)
	hardenGroup(c)
	return c
}

// targetApp walks up from cmd to find the app sub-root it lives under.
func targetApp(cmd *cobra.Command) (config.App, bool) {
	for c := cmd; c != nil; c = c.Parent() {
		if name, ok := c.Annotations["app"]; ok {
			return config.App(name), true
		}
	}
	return "", false
}

func validSortDir(v string) bool {
	switch v {
	case "default", "ascending", "descending":
		return true
	}
	return false
}

// Execute runs the root command and translates errors into the JSON error
// envelope + stable exit code.
func Execute() {
	root, app := NewRoot()
	err := root.Execute()
	if err == nil {
		return
	}
	os.Exit(int(app.Out.EmitError(classifyError(err))))
}

// classifyError types whatever Execute returned. Anything cobra hands back that
// is not already a CLIError is a usage error: unknown command (including a
// missing or misspelled app), unknown flag, bad positional args.
func classifyError(err error) *output.CLIError {
	var cliErr *output.CLIError
	if errors.As(err, &cliErr) {
		return cliErr
	}
	msg := err.Error()
	hint := "run `arrctl commands` to list every command"
	if strings.HasPrefix(msg, "unknown command") {
		hint = "the first positional must be the app: `arrctl radarr <group> <verb>` or " +
			"`arrctl sonarr <group> <verb>`; there is no default app"
	}
	return output.UsageErrorHint(msg, hint)
}
