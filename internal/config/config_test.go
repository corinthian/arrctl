package config

import (
	"errors"
	"os"
	"path/filepath"
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
timeout = "45s"
`

// The file supplies the URL, the Keychain supplies the key: the documented
// resting state, with no key ever written to disk.
func TestResolveFileThenKeychain(t *testing.T) {
	stubKeychain(t, map[string]string{"radarrctl/apikey": "KEYCHAIN-VALUE"})
	l, err := LoadFile(writeConfig(t, sample))
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
	l, err := LoadFile(writeConfig(t, sample))
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
	l, _ := LoadFile(writeConfig(t, sample))

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
