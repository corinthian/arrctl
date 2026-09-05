// Package config resolves per-app runtime configuration from, in precedence
// order: CLI flags > environment > ~/.config/arrctl/config.toml > macOS
// Keychain (the API key only). The file holds URLs; keys live in the Keychain.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/zalando/go-keyring"
)

// App names the target instance. It is the first positional argument.
type App string

const (
	Radarr App = "radarr"
	Sonarr App = "sonarr"
)

// Apps is the closed set of app names, in stable order.
var Apps = []App{Radarr, Sonarr}

// KeychainService is the Keychain service name for an app's API key: service
// `radarrctl` / `sonarrctl`, account `apikey` (ruled 2026-09-04 by what is
// already stored; no fallback list).
func (a App) KeychainService() string { return string(a) + "ctl" }

// KeychainAccount is the Keychain account name for every app's API key.
const KeychainAccount = "apikey"

// DefaultTimeout applies when neither flags, env nor file set one.
const DefaultTimeout = 30 * time.Second

// AppConfig is one app's resolved settings.
type AppConfig struct {
	App      App
	URL      string
	APIKey   string
	Insecure bool
	Timeout  time.Duration

	// KeySource records where the API key came from, for `config show`/`path`.
	// Never carries the key itself.
	KeySource string
}

// fileApp is the on-disk shape of a `[radarr]` / `[sonarr]` table.
type fileApp struct {
	URL      string `toml:"url,omitempty"`
	APIKey   string `toml:"apikey,omitempty"`
	Insecure bool   `toml:"insecure,omitempty"`
}

// fileDefaults is the on-disk `[defaults]` table.
type fileDefaults struct {
	Timeout string `toml:"timeout,omitempty"`
}

// File is the whole config.toml.
type File struct {
	Radarr   fileApp      `toml:"radarr"`
	Sonarr   fileApp      `toml:"sonarr"`
	Defaults fileDefaults `toml:"defaults"`
}

func (f *File) app(a App) fileApp {
	if a == Sonarr {
		return f.Sonarr
	}
	return f.Radarr
}

// Flags carries the raw CLI flag values that override env and file.
type Flags struct {
	URL         string
	APIKey      string
	Insecure    bool
	InsecureSet bool
	Timeout     string
	ConfigPath  string
}

// Loaded is the outcome of reading the config file: the parsed content plus
// where it came from ("" when no file exists).
type Loaded struct {
	File   File
	Source string
}

// LoadFile reads config.toml. A missing DEFAULT file is not an error — every
// setting has an env/flag path. An explicit source is different: `--config` and
// `$ARRCTL_CONFIG` are the caller saying which file to use, so an unusable one
// fails rather than falling through to the default. Silently reading a
// different file than the caller named is the worst outcome available, because
// the wrong instance answers and nothing says so.
//
// tolerate is for the read-only diagnostics only — `config path` exists to
// explain this very failure, and `config init --config /new/file.toml` targets
// a file that does not exist yet. Nothing that talks to an instance sets it.
func LoadFile(explicit string, tolerate bool) (*Loaded, error) {
	l := &Loaded{}
	r := resolveConfigPath(explicit)
	if r.Err != nil {
		if tolerate {
			return l, nil
		}
		return nil, r.Err
	}
	if r.Path == "" {
		return l, nil
	}
	b, err := os.ReadFile(r.Path)
	if err != nil {
		if tolerate {
			return l, nil
		}
		return nil, fmt.Errorf("reading %s: %w", r.Path, err)
	}
	if err := toml.Unmarshal(b, &l.File); err != nil {
		if tolerate {
			return l, nil
		}
		return nil, fmt.Errorf("parsing %s: %w", r.Path, err)
	}
	l.Source = r.Path
	return l, nil
}

// keyLookup is the Keychain reader, swappable in tests.
var keyLookup = func(service, account string) (string, error) {
	return keyring.Get(service, account)
}

// Resolve builds one app's config from flags > env > file > Keychain. The
// Keychain is consulted for the API key only, and only when nothing higher
// supplied one.
func Resolve(a App, f Flags, l *Loaded) (*AppConfig, error) {
	if l == nil {
		l = &Loaded{}
	}
	fa := l.File.app(a)
	c := &AppConfig{App: a}

	// 3. file
	c.URL = fa.URL
	c.APIKey = fa.APIKey
	c.Insecure = fa.Insecure
	if c.APIKey != "" {
		c.KeySource = "config.toml"
	}

	// 2. env overrides file
	up := "ARRCTL_" + strings.ToUpper(string(a)) + "_"
	if v := os.Getenv(up + "URL"); v != "" {
		c.URL = v
	}
	if v := os.Getenv(up + "APIKEY"); v != "" {
		c.APIKey = v
		c.KeySource = up + "APIKEY"
	}
	if v := os.Getenv(up + "INSECURE"); v != "" {
		c.Insecure = truthy(v)
	}

	// 1. flags override everything
	if f.URL != "" {
		c.URL = f.URL
	}
	if f.APIKey != "" {
		c.APIKey = f.APIKey
		c.KeySource = "--apikey"
	}
	if f.InsecureSet {
		c.Insecure = f.Insecure
	}

	// 4. Keychain, key only, lowest precedence.
	if c.APIKey == "" {
		k, err := keyLookup(a.KeychainService(), KeychainAccount)
		if err == nil && k != "" {
			c.APIKey = k
			c.KeySource = "keychain:" + a.KeychainService() + "/" + KeychainAccount
		}
	}

	c.Timeout = resolveTimeout(f.Timeout, l.File.Defaults.Timeout)

	if c.URL == "" {
		return nil, fmt.Errorf("no url for %s; set --url, %sURL, or [%s] url in %s",
			a, up, a, DisplayConfigPath())
	}
	if err := validateURL(c.URL); err != nil {
		return nil, err
	}
	c.URL = strings.TrimRight(c.URL, "/")
	return c, nil
}

// KeyHint is the recovery line for a missing or rejected key. It never contains
// a key value.
func (a App) KeyHint() string {
	return "security add-generic-password -s " + a.KeychainService() +
		" -a " + KeychainAccount + " -w <key> -U"
}

func resolveTimeout(flag, file string) time.Duration {
	for _, s := range []string{flag, os.Getenv("ARRCTL_TIMEOUT"), file} {
		if s == "" {
			continue
		}
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	return DefaultTimeout
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// validateURL enforces the trust boundary on where API traffic can go. Unlike
// traktctl (public API, https-only), a Servarr instance is normally a LAN box
// reached over plain http, so http is allowed to any host. Userinfo in the URL
// is rejected outright: it has no legitimate use here and is a credential-leak
// vector.
func validateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url %q: %w", raw, err)
	}
	if u.User != nil {
		return fmt.Errorf("url %q must not contain userinfo", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q has no host", raw)
	}
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("url %q must use http or https", raw)
	}
}

// Resolution describes which config file arrctl would read and why. It is the
// reporting form, for `config path` and `config show`; LoadFile is the
// enforcing form.
type Resolution struct {
	// Path is the file that will be read, "" when there is none to read.
	Path string
	// Explicit records that the path came from --config or $ARRCTL_CONFIG
	// rather than the default location.
	Explicit bool
	// Source names where the path came from, for error text.
	Source string
	// Err says why an unusable path is unusable. Only a MISSING default is
	// silent; everything else — a named path that is absent, a directory, or
	// any read failure that is not "no such file" — lands here.
	Err error
}

// Found reports whether a readable config file was resolved.
func (r Resolution) Found() bool { return r.Path != "" && r.Err == nil }

// resolveConfigPath picks the config file: --config, then $ARRCTL_CONFIG, then
// ~/.config/arrctl/config.toml. The first two are explicit and never fall
// through to the next candidate. An empty $ARRCTL_CONFIG is unset, not a path.
func resolveConfigPath(explicit string) Resolution {
	if p := strings.TrimSpace(explicit); p != "" {
		return statConfig(p, true, "--config")
	}
	if p := strings.TrimSpace(os.Getenv("ARRCTL_CONFIG")); p != "" {
		return statConfig(p, true, "$ARRCTL_CONFIG")
	}
	p, err := DefaultConfigPath()
	if err != nil {
		return Resolution{Source: "the default config path"}
	}
	r := statConfig(p, false, "the default config path")
	if r.Err != nil && errors.Is(r.Err, os.ErrNotExist) {
		// The default file is allowed to be absent: every setting has a
		// flag or environment path.
		return Resolution{Source: r.Source}
	}
	return r
}

func statConfig(path string, explicit bool, source string) Resolution {
	r := Resolution{Path: path, Explicit: explicit, Source: source}
	st, err := os.Stat(path)
	switch {
	case err != nil:
		r.Path, r.Err = "", fmt.Errorf("config file %s (from %s) cannot be used: %w", path, source, err)
	case st.IsDir():
		r.Path, r.Err = "", fmt.Errorf("config path %s (from %s) is a directory, not a file", path, source)
	}
	return r
}

// ResolvedConfigPath reports which config file would be read, whether the
// caller named it, and why it cannot be used if it cannot.
func ResolvedConfigPath(explicit string) Resolution { return resolveConfigPath(explicit) }

// DefaultConfigPath returns ~/.config/arrctl/config.toml (does not create it).
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "arrctl", "config.toml"), nil
}

// DisplayConfigPath is DefaultConfigPath for error text, degrading to the
// literal when the home directory cannot be resolved.
func DisplayConfigPath() string {
	if p, err := DefaultConfigPath(); err == nil {
		return p
	}
	return "~/.config/arrctl/config.toml"
}

// WriteFile writes fc to path (0600), creating parent dirs. It refuses to
// overwrite an existing file unless force is set. An apikey field is never
// written: `config init` holds URLs only, keys live in the Keychain.
func WriteFile(path string, fc File, force bool) error {
	fc.Radarr.APIKey = ""
	fc.Sonarr.APIKey = ""
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config already exists at %s (use --force to overwrite)", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := toml.Marshal(fc)
	if err != nil {
		return err
	}
	header := "# arrctl configuration — written by `arrctl config init`\n" +
		"# URLs only. API keys live in the macOS Keychain:\n" +
		"#   security add-generic-password -s radarrctl -a apikey -w <key> -U\n" +
		"#   security add-generic-password -s sonarrctl -a apikey -w <key> -U\n\n"
	return AtomicWriteFile(path, append([]byte(header), b...))
}
