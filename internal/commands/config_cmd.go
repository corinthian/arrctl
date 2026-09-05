package commands

import (
	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/spf13/cobra"
)

// newConfigCmd builds the app-independent `config` group. It sits at the root,
// not under an app sub-root, because it describes both apps at once.
func newConfigCmd(app *App) *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "Local configuration bootstrap"}
	root.AddCommand(app.configInit(), app.configShow(), app.configPath())
	hardenGroup(root)
	return root
}

// configInit writes config.toml. It holds URLs only: an API key is never
// written, whatever the caller passes.
func (a *App) configInit() *cobra.Command {
	var radarrURL, sonarrURL, timeout string
	var sonarrInsecure, radarrInsecure, force bool
	c := &cobra.Command{
		Use:         "init",
		Short:       "Write config.toml (URLs only; keys stay in the Keychain)",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annTolerateBadConfig: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if radarrURL == "" && sonarrURL == "" {
				return output.UsageErrorHint(
					"nothing to write: pass --radarr-url and/or --sonarr-url",
					"e.g. arrctl config init --radarr-url http://HOST --sonarr-url https://HOST --sonarr-insecure")
			}
			f := config.File{}
			f.Radarr.URL = radarrURL
			f.Radarr.Insecure = radarrInsecure
			f.Sonarr.URL = sonarrURL
			f.Sonarr.Insecure = sonarrInsecure
			f.Defaults.Timeout = timeout

			path := a.Flags.ConfigPath
			if path == "" {
				p, err := config.DefaultConfigPath()
				if err != nil {
					return output.NewError(output.CodeBadConfig, "resolving config path: "+err.Error())
				}
				path = p
			}
			if err := config.WriteFile(path, f, force); err != nil {
				return output.NewError(output.CodeBadConfig, err.Error())
			}
			return a.emitLocal(map[string]interface{}{
				"written_to": path,
				"radarr":     map[string]interface{}{"url": radarrURL, "insecure": radarrInsecure},
				"sonarr":     map[string]interface{}{"url": sonarrURL, "insecure": sonarrInsecure},
				"apikeys":    "not written; store them with: security add-generic-password -s radarrctl -a apikey -w <key> -U",
			}, "Wrote "+path)
		},
	}
	c.Flags().StringVar(&radarrURL, "radarr-url", "", "Radarr base URL")
	c.Flags().StringVar(&sonarrURL, "sonarr-url", "", "Sonarr base URL")
	c.Flags().BoolVar(&radarrInsecure, "radarr-insecure", false, "skip TLS verification for Radarr")
	c.Flags().BoolVar(&sonarrInsecure, "sonarr-insecure", false, "skip TLS verification for Sonarr")
	c.Flags().StringVar(&timeout, "timeout-default", "", "default HTTP timeout, e.g. 30s")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return c
}

// configShow prints the resolved settings for both apps. The API key is never
// printed: only whether one was found and where it came from.
func (a *App) configShow() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show resolved settings for both apps (keys redacted)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := map[string]interface{}{
				"config_in_use": config.ResolvedConfigPath(a.Flags.ConfigPath).Path,
			}
			for _, app := range config.Apps {
				row := map[string]interface{}{}
				cfg, err := config.Resolve(app, config.Flags{
					URL: a.Flags.URL, APIKey: a.Flags.APIKey,
					Insecure: a.Flags.Insecure, InsecureSet: a.insecureSet,
					Timeout: a.Flags.Timeout, ConfigPath: a.Flags.ConfigPath,
				}, a.Loaded)
				if err != nil {
					row["error"] = err.Error()
					out[string(app)] = row
					continue
				}
				row["url"] = cfg.URL
				row["insecure"] = cfg.Insecure
				row["timeout"] = cfg.Timeout.String()
				row["apikey"] = redact(cfg.APIKey)
				row["apikey_source"] = cfg.KeySource
				out[string(app)] = row
			}
			return a.emitLocal(out, "")
		},
	}
}

// configPath prints where config and keys are read from. No secrets.
func (a *App) configPath() *cobra.Command {
	return &cobra.Command{
		Use:         "path",
		Short:       "Show resolved config file and Keychain locations",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annTolerateBadConfig: "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := config.ResolvedConfigPath(a.Flags.ConfigPath)
			def, _ := config.DefaultConfigPath()
			keys := map[string]interface{}{}
			for _, app := range config.Apps {
				keys[string(app)] = map[string]string{
					"service": app.KeychainService(),
					"account": config.KeychainAccount,
					"store":   "macOS login Keychain",
				}
			}
			return a.emitLocal(map[string]interface{}{
				"config_in_use":   resolved.Path,
				"config_default":  def,
				"config_found":    resolved.Found(),
				"config_explicit": resolved.Explicit,
				"config_error":    errText(resolved.Err),
				"apikeys":         keys,
			}, "")
		},
	}
}

// errText renders a resolution error for the payload, "" when there is none.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// redact reports a key's presence without disclosing any of it.
func redact(k string) string {
	if k == "" {
		return "(none found)"
	}
	return "***redacted***"
}
