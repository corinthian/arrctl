package commands

import (
	"strconv"
	"strings"

	"github.com/corinthian/arrctl/internal/config"
	"github.com/corinthian/arrctl/internal/output"
	"github.com/corinthian/arrctl/internal/xduration"
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
	var radarrURL, sonarrURL string
	var timeoutDefault int
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
			if cmd.Flags().Changed("timeout-default") {
				if _, err := xduration.Parse(strconv.Itoa(timeoutDefault), "--timeout-default"); err != nil {
					return output.NewError(output.CodeBadRequest, err.Error())
				}
				f.Defaults.Timeout = &timeoutDefault
			}

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
				"apikeys":    "not written; " + initKeyAdvice(a.Loaded),
			}, "Wrote "+path)
		},
	}
	c.Flags().StringVar(&radarrURL, "radarr-url", "", "Radarr base URL")
	c.Flags().StringVar(&sonarrURL, "sonarr-url", "", "Sonarr base URL")
	c.Flags().BoolVar(&radarrInsecure, "radarr-insecure", false, "skip TLS verification for Radarr")
	c.Flags().BoolVar(&sonarrInsecure, "sonarr-insecure", false, "skip TLS verification for Sonarr")
	c.Flags().IntVar(&timeoutDefault, "timeout-default", 0, "default HTTP timeout in whole seconds, 1-86400")
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
			reportTokenStore(out, a.Loaded)
			for _, app := range config.Apps {
				row := map[string]interface{}{}
				cfg, err := config.Resolve(app, config.Flags{
					URL: a.Flags.URL, APIKey: a.Flags.APIKey,
					Insecure: a.Flags.Insecure, InsecureSet: a.insecureSet,
					Timeout: a.Flags.Timeout, TimeoutSet: a.timeoutSet, ConfigPath: a.Flags.ConfigPath,
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
				if cfg.KeychainErr != "" {
					row["keychain_error"] = cfg.KeychainErr
				}
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
			mode, _ := config.ResolveTokenStore(a.Loaded)
			keys := map[string]interface{}{}
			for _, app := range config.Apps {
				// The Keychain failure, if any, as the key would actually be
				// resolved; a resolve that fails for another reason (no url)
				// has nothing to add here.
				var kerr string
				if cfg, err := config.Resolve(app, config.Flags{
					URL: a.Flags.URL, APIKey: a.Flags.APIKey,
					Insecure: a.Flags.Insecure, InsecureSet: a.insecureSet,
					Timeout: a.Flags.Timeout, TimeoutSet: a.timeoutSet, ConfigPath: a.Flags.ConfigPath,
				}, a.Loaded); err == nil {
					kerr = cfg.KeychainErr
				}
				if mode == config.TokenStoreFile {
					keys[string(app)] = map[string]string{
						"store": "ARRCTL_" + strings.ToUpper(string(app)) + "_APIKEY or [" + string(app) + "] apikey in config.toml",
					}
					continue
				}
				entry := map[string]string{
					"service": app.KeychainService(),
					"account": config.KeychainAccount,
					"store":   "macOS login Keychain",
				}
				if kerr != "" {
					entry["keychain_error"] = kerr
				}
				keys[string(app)] = entry
			}
			out := map[string]interface{}{
				"config_in_use":   resolved.Path,
				"config_default":  def,
				"config_found":    resolved.Found(),
				"config_explicit": resolved.Explicit,
				"config_error":    errText(resolved.Err),
				"apikeys":         keys,
			}
			reportTokenStore(out, a.Loaded)
			return a.emitLocal(out, "")
		},
	}
}

// reportTokenStore adds the key store mode to a diagnostic payload, or the
// reason it is invalid.
func reportTokenStore(out map[string]interface{}, l *config.Loaded) {
	mode, err := config.ResolveTokenStore(l)
	if err != nil {
		out["token_store_error"] = err.Error()
		return
	}
	out["token_store"] = mode
}

// initKeyAdvice says where API keys go under the current key store mode.
func initKeyAdvice(l *config.Loaded) string {
	if mode, _ := config.ResolveTokenStore(l); mode == config.TokenStoreFile {
		return "token_store is file: set ARRCTL_RADARR_APIKEY / ARRCTL_SONARR_APIKEY, or add apikey to config.toml by hand"
	}
	return "store them with: security add-generic-password -s radarrctl -a apikey -w <key> -U"
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
