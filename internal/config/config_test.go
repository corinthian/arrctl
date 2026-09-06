package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubKeychain replaces the Keychain reader for the duration of a test. No test
// in this package ever reads a real key.
func stubKeychain(t *testing.T, vals map[string]string) {
	t.Helper()
	orig := keyLookup
	keyLookup = func(service, account string) (string, error) {
		if v, ok := vals[service+"/"+account]; ok {
			return v, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { keyLookup = orig })
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const sample = `
[radarr]
url = "http://radarr.example"

[sonarr]
url = "https://sonarr.example"
insecure = true

[defaults]
timeout = 45
`

// The file supplies the URL, the Keychain supplies the key: the documented
// resting state, with no key ever written to disk.
func TestResolveFileThenKeychain(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "KEYCHAIN-VALUE"})
	l, err := LoadFile(writeConfig(t, sample), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "http://radarr.example" {
		t.Errorf("url = %q", c.URL)
	}
	if c.APIKey != "KEYCHAIN-VALUE" {
		t.Errorf("key not taken from the Keychain")
	}
	if c.KeySource != "keychain:radarrctl/apikey" {
		t.Errorf("key source = %q", c.KeySource)
	}
	if c.Timeout != 45*time.Second {
		t.Errorf("timeout = %s, want 45s", c.Timeout)
	}
}

// Env beats the file; flags beat env. Both must beat the Keychain.
func TestResolvePrecedence(t *testing.T) {
	stubKeychain(t, map[string]string{"sonarrctl/apikey": "FROM-KEYCHAIN"})
	l, err := LoadFile(writeConfig(t, sample), false)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("ARRCTL_SONARR_URL", "https://env.example")
	t.Setenv("ARRCTL_SONARR_APIKEY", "FROM-ENV")
	c, err := Resolve(Sonarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://env.example" || c.APIKey != "FROM-ENV" {
		t.Errorf("env did not override the file: %s / %s", c.URL, c.KeySource)
	}

	c, err = Resolve(Sonarr, Flags{URL: "https://flag.example", APIKey: "FROM-FLAG"}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://flag.example" || c.APIKey != "FROM-FLAG" {
		t.Errorf("flags did not override env: %s / %s", c.URL, c.KeySource)
	}
}

// insecure = true in the file survives an untouched --insecure flag. Without
// InsecureSet the flag's false default would silently re-enable verification
// against an instance with a known-bad certificate.
func TestInsecureFromFileSurvivesUnsetFlag(t *testing.T) {
	stubKeychain(t, map[string]string{"sonarrctl/apikey": "K"})
	l, _ := LoadFile(writeConfig(t, sample), false)

	c, err := Resolve(Sonarr, Flags{Insecure: false, InsecureSet: false}, l)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Insecure {
		t.Error("file's insecure=true was overridden by an untouched flag")
	}

	c, err = Resolve(Sonarr, Flags{Insecure: false, InsecureSet: true}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.Insecure {
		t.Error("an explicit --insecure=false did not take effect")
	}
}

// Radarr is reached over plain http on a LAN name; that must be allowed, unlike
// traktctl's https-only public API rule.
func TestPlainHTTPToLANHostIsAllowed(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	c, err := Resolve(Radarr, Flags{URL: "http://some-host.example"}, &Loaded{})
	if err != nil {
		t.Fatalf("plain http rejected: %v", err)
	}
	if c.URL != "http://some-host.example" {
		t.Errorf("url = %q", c.URL)
	}
}

func TestBadURLsRejected(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	for _, raw := range []string{
		"ftp://host",          // wrong scheme
		"http://user:pw@host", // userinfo is a credential-leak vector
		"notaurl",             // no host
	} {
		if _, err := Resolve(Radarr, Flags{URL: raw}, &Loaded{}); err == nil {
			t.Errorf("Resolve accepted %q", raw)
		}
	}
}

func TestMissingURLIsAnError(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	if _, err := Resolve(Radarr, Flags{}, &Loaded{}); err == nil {
		t.Error("Resolve accepted an empty URL")
	}
}

// A missing Keychain entry is not fatal at resolve time: the client raises
// BAD_CONFIG when the call is actually made, with the storage hint.
func TestMissingKeyIsNotAResolveError(t *testing.T) {
	stubKeychain(t, map[string]string{})
	c, err := Resolve(Radarr, Flags{URL: "http://host.example"}, &Loaded{})
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "" {
		t.Error("a key materialised from nowhere")
	}
}

func TestKeychainNamesMatchWhatIsStored(t *testing.T) {
	if Radarr.KeychainService() != "radarrctl" || Sonarr.KeychainService() != "sonarrctl" {
		t.Errorf("service names drifted: %s / %s", Radarr.KeychainService(), Sonarr.KeychainService())
	}
	if KeychainAccount != "apikey" {
		t.Errorf("account = %q, want apikey", KeychainAccount)
	}
}

// `config init` holds URLs only. A key handed to it must never reach disk.
func TestWriteFileNeverWritesAKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	f := File{}
	f.Radarr.URL = "http://host.example"
	f.Radarr.APIKey = "SHOULD-NOT-BE-WRITTEN"
	if err := WriteFile(p, f, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" {
		t.Fatal("empty config written")
	}
	if contains(string(b), "SHOULD-NOT-BE-WRITTEN") {
		t.Error("config init wrote an API key to disk")
	}
	// And it refuses to clobber without --force.
	if err := WriteFile(p, f, false); err == nil {
		t.Error("WriteFile overwrote an existing config without force")
	}
	if err := WriteFile(p, f, true); err != nil {
		t.Errorf("--force did not overwrite: %v", err)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// isolateHome points the default config path at an empty directory, so a test
// asserting on explicit paths cannot be answered by the developer's real
// ~/.config/arrctl/config.toml.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
}

// An explicit config source is authoritative. Silently falling back to the
// default meant `--config /path/that/moved` read the wrong instance's settings
// and reported success, which is the worst outcome available: the caller
// believes it targeted one thing and targeted another.
func TestExplicitConfigMissingIsError(t *testing.T) {
	isolateHome(t)
	if _, err := LoadFile("/nonexistent/arrctl/x.toml", false); err == nil {
		t.Fatal("a missing --config path fell through to the default")
	}
}

func TestExplicitConfigDirectoryIsError(t *testing.T) {
	isolateHome(t)
	if _, err := LoadFile(t.TempDir(), false); err == nil {
		t.Fatal("a directory was accepted as a config file")
	}
}

// $ARRCTL_CONFIG is an explicit source too, and gets the same treatment.
func TestEnvConfigMissingIsError(t *testing.T) {
	isolateHome(t)
	t.Setenv("ARRCTL_CONFIG", "/nonexistent/arrctl/env.toml")
	if _, err := LoadFile("", false); err == nil {
		t.Fatal("a missing $ARRCTL_CONFIG path fell through to the default")
	}
	r := ResolvedConfigPath("")
	if !r.Explicit || r.Err == nil {
		t.Errorf("resolution = %+v, want explicit with an error", r)
	}
}

// An empty $ARRCTL_CONFIG is unset, not a path to the empty string.
func TestEmptyEnvConfigCountsAsUnset(t *testing.T) {
	isolateHome(t)
	l, err := LoadFile("", false)
	if err != nil {
		t.Fatalf("an empty $ARRCTL_CONFIG was treated as an explicit path: %v", err)
	}
	if l.Source != "" {
		t.Errorf("source = %q, want none", l.Source)
	}
}

// The default file may be absent silently — every setting has a flag/env path.
func TestMissingDefaultConfigStaysSilent(t *testing.T) {
	isolateHome(t)
	l, err := LoadFile("", false)
	if err != nil {
		t.Fatalf("a missing default config errored: %v", err)
	}
	if l.Source != "" {
		t.Errorf("source = %q, want none", l.Source)
	}
	if r := ResolvedConfigPath(""); r.Explicit || r.Err != nil || r.Path != "" {
		t.Errorf("resolution = %+v, want a silent empty default", r)
	}
}

// A read error that is not "no such file" is a config error wherever it
// happens: reading it as "no file" would silently ignore settings that exist.
func TestUnreadableConfigIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file regardless")
	}
	isolateHome(t)
	p := writeConfig(t, sample)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if _, err := LoadFile(p, false); err == nil {
		t.Fatal("an unreadable config was read as no config")
	}
}

// A good explicit path still resolves and still parses.
func TestExplicitConfigIsRead(t *testing.T) {
	isolateHome(t)
	p := writeConfig(t, sample)
	l, err := LoadFile(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if l.Source != p || l.File.Radarr.URL != "http://radarr.example" {
		t.Errorf("loaded %+v from %q", l.File, l.Source)
	}
	r := ResolvedConfigPath(p)
	if !r.Explicit || r.Err != nil || r.Path != p {
		t.Errorf("resolution = %+v", r)
	}
}

// The tolerant form is what the read-only diagnostics use: `config path` exists
// to explain exactly this failure, and `config init --config /new/file.toml`
// targets a file that does not exist yet. Neither may die on it.
func TestTolerantLoadSurvivesBadExplicitPath(t *testing.T) {
	isolateHome(t)
	if _, err := LoadFile("/nonexistent/arrctl/x.toml", true); err != nil {
		t.Fatalf("the tolerant form failed on a missing explicit path: %v", err)
	}
	// A path it cannot use is forgiven. A file whose contents will not parse
	// is not: the file is there, it was read, and it is wrong.
	bad := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(bad, []byte("this is not = = toml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(bad, true); err == nil {
		t.Fatal("the tolerant form forgave an unparseable file")
	}
}

// Tolerance covers an unusable path, not unusable contents. `config path`
// exists to explain a broken path and must survive one; nothing has any reason
// to keep going with a file it could read and could not understand.
func TestTolerateDoesNotForgiveParseFailure(t *testing.T) {
	isolateHome(t)
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[radarr\nurl = "), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := LoadFile(p, true)
	if err == nil {
		t.Fatalf("unparseable TOML was forgiven under tolerance: %+v", l)
	}
	if !strings.Contains(err.Error(), p) {
		t.Errorf("error does not name the file: %v", err)
	}
}

// An unreadable-but-present file is a path or I/O failure, not a statement
// about the file's contents, so tolerance still forgives it.
func TestUnreadableFileStaysForgivenUnderTolerance(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file regardless")
	}
	isolateHome(t)
	p := writeConfig(t, sample)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if _, err := LoadFile(p, true); err != nil {
		t.Fatalf("an unreadable file was not forgiven under tolerance: %v", err)
	}
}

// A base URL carrying a query or a fragment cannot have been working: the
// request builder appended its own path and query to it, producing a URL whose
// path was empty and whose query was the caller's. Fail at resolution, where
// the message can name the problem, rather than issuing a nonsense request.
func TestValidateURLRejectsQueryAndFragment(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	for _, raw := range []string{
		"http://host?apikey=leaked",
		"http://host/radarr?x=1",
		"http://host#frag",
		"http://host/radarr#frag",
	} {
		if _, err := Resolve(Radarr, Flags{URL: raw}, &Loaded{}); err == nil {
			t.Errorf("Resolve accepted %q", raw)
		}
	}
}

// A path is NOT a query: http://host/radarr is the ordinary reverse-proxy base
// for a Servarr instance and has to keep working.
func TestReverseProxyPathBaseIsAllowed(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	c, err := Resolve(Radarr, Flags{URL: "http://host/radarr"}, &Loaded{})
	if err != nil {
		t.Fatalf("a reverse-proxy base was rejected: %v", err)
	}
	if c.URL != "http://host/radarr" {
		t.Errorf("url = %q", c.URL)
	}
}

// The --timeout flag, once explicitly set, is authoritative: an invalid value
// is always an error naming --timeout, whether it is a duration string, an
// out-of-range integer, or an explicitly empty value. Env and file are never
// consulted to rescue it.
func TestResolveTimeoutFlagInvalidIsBadRequest(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	for _, v := range []string{"30s", "abc", "0", "-5", "90000", "", " 30 "} {
		_, err := Resolve(Radarr, Flags{TimeoutSet: true, Timeout: v}, &Loaded{})
		if err == nil {
			t.Errorf("--timeout %q: accepted, want an error", v)
			continue
		}
		if !strings.Contains(err.Error(), "--timeout") {
			t.Errorf("--timeout %q: error %q does not name --timeout", v, err.Error())
		}
	}
}

// An invalid $ARRCTL_TIMEOUT is an error naming the env var; an empty value is
// unset and falls through.
func TestResolveTimeoutEnvInvalidNamesEnv(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	t.Setenv("ARRCTL_TIMEOUT", "abc")
	_, err := Resolve(Radarr, Flags{}, &Loaded{})
	if err == nil || !strings.Contains(err.Error(), "$ARRCTL_TIMEOUT") {
		t.Fatalf("invalid $ARRCTL_TIMEOUT: got %v, want an error naming $ARRCTL_TIMEOUT", err)
	}

	t.Setenv("ARRCTL_TIMEOUT", "")
	c, err := Resolve(Radarr, Flags{URL: "http://host.example"}, &Loaded{})
	if err != nil {
		t.Fatalf("empty $ARRCTL_TIMEOUT should be treated as unset: %v", err)
	}
	if c.Timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want the default %s", c.Timeout, DefaultTimeout)
	}
}

// A rejected file value names "config timeout (<path>)".
func TestResolveTimeoutFileInvalidNamesConfigTimeout(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	path := writeConfig(t, "[defaults]\ntimeout = 90000\n")
	l, err := LoadFile(path, false)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	_, err = Resolve(Radarr, Flags{}, l)
	if err == nil {
		t.Fatal("out-of-range file timeout accepted")
	}
	want := "config timeout (" + path + ")"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// The higher-priority flag wins even when it is invalid: a valid file value
// never rescues it.
func TestInvalidFlagBeatsValidFile(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	path := writeConfig(t, "[defaults]\ntimeout = 30\n")
	l, err := LoadFile(path, false)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	_, err = Resolve(Radarr, Flags{TimeoutSet: true, Timeout: "abc"}, l)
	if err == nil {
		t.Fatal("invalid --timeout was rescued by a valid file value")
	}
}

// A TOML timeout that is not an integer is BAD_CONFIG naming "config timeout
// (<path>)", never a bare go-toml type-mismatch message.
func TestTOMLTimeoutStringIsBadConfig(t *testing.T) {
	for _, body := range []string{
		"[defaults]\ntimeout = \"30s\"\n",
		"[defaults]\ntimeout = \"30\"\n",
		"[defaults]\ntimeout = 30.0\n",
		"[defaults]\ntimeout = true\n",
		"[defaults]\ntimeout = [1, 2]\n",
		"[defaults]\ntimeout = { a = 1 }\n",
	} {
		path := writeConfig(t, body)
		_, err := LoadFile(path, false)
		if err == nil {
			t.Errorf("body %q: accepted, want an error", body)
			continue
		}
		want := "config timeout (" + path + ")"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("body %q: error %q does not contain %q", body, err.Error(), want)
		}
	}
}

// The only accepted file form is a bare TOML integer.
func TestTOMLTimeoutIntegerAccepted(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "K"})
	path := writeConfig(t, "[radarr]\nurl = \"http://host.example\"\n\n[defaults]\ntimeout = 30\n")
	l, err := LoadFile(path, false)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("timeout = %s, want 30s", c.Timeout)
	}
}

// WriteFile, now built on atomicfile.Write, keeps the documented symlink
// behaviour: the rename replaces the symlink itself, never writing through it
// to whatever it points at.
func TestWriteFileReplacesSymlinkLeavingTargetUntouched(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.toml")
	if err := os.WriteFile(target, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	f := File{}
	f.Radarr.URL = "http://host.example"
	if err := WriteFile(link, f, true); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	targetBody, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(targetBody) != "do-not-touch" {
		t.Errorf("the symlink target was written through: %q", targetBody)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Error("WriteFile did not replace the symlink itself")
	}
}
