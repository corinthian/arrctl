package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/corinthian/arrctl/internal/keychain"
)

func countKeychain(t *testing.T) *int {
	t.Helper()
	n := 0
	orig := keyLookup
	keyLookup = func(string, string) (string, error) { n++; return "FROM-KEYCHAIN", nil }
	t.Cleanup(func() { keyLookup = orig })
	return &n
}

func TestResolveFileModeNeverCallsKeychain(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	n := countKeychain(t)
	l, err := LoadFile(writeConfig(t, sample+"token_store = \"file\"\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 0 {
		t.Errorf("keyLookup called %d times in file mode", *n)
	}
	if c.APIKey != "" || c.KeySource != "" || c.TokenStore != TokenStoreFile {
		t.Errorf("got key source %q, mode %q; want no key, file mode", c.KeySource, c.TokenStore)
	}
}

func TestResolveKeychainModeCallsKeychain(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	n := countKeychain(t)
	l, err := LoadFile(writeConfig(t, sample), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 1 || c.APIKey != "FROM-KEYCHAIN" || c.TokenStore != TokenStoreKeychain {
		t.Errorf("calls=%d key source=%q mode=%q", *n, c.KeySource, c.TokenStore)
	}
}

// TestResolveProceedsWhenKeychainTimesOut: the bound is proven in
// internal/keychain; here a lookup that reports the timeout must leave Resolve
// carrying on with no key.
func TestResolveProceedsWhenKeychainTimesOut(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	orig := keyLookup
	keyLookup = func(string, string) (string, error) {
		return "", fmt.Errorf("%w: timed out after 5s", keychain.ErrUnavailable)
	}
	t.Cleanup(func() { keyLookup = orig })
	l, err := LoadFile(writeConfig(t, sample), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatalf("Resolve = %v, want success with no key", err)
	}
	if c.APIKey != "" || c.URL != "http://radarr.example" {
		t.Errorf("key source %q url %q", c.KeySource, c.URL)
	}
}

func TestTokenStoreEnvBeatsFile(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "keychain")
	l, err := LoadFile(writeConfig(t, sample+"token_store = \"file\"\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := ResolveTokenStore(l); err != nil || m != TokenStoreKeychain {
		t.Errorf("mode = %q, %v; want keychain from env", m, err)
	}
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	if m, err := ResolveTokenStore(l); err != nil || m != TokenStoreFile {
		t.Errorf("mode = %q, %v; want file from config", m, err)
	}
	if m, err := ResolveTokenStore(nil); err != nil || m != TokenStoreKeychain {
		t.Errorf("default mode = %q, %v; want keychain", m, err)
	}
}

func TestTokenStoreInvalid(t *testing.T) {
	cases := []struct{ name, env, extra, wantSrc string }{
		{"file", "", "token_store = \"disk\"\n", "config token_store"},
		{"env", "disk", "", "$ARRCTL_TOKEN_STORE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ARRCTL_TOKEN_STORE", c.env)
			stubKeychain(t, nil)
			l, err := LoadFile(writeConfig(t, sample+c.extra), false)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Resolve(Radarr, Flags{}, l)
			if err == nil {
				t.Fatal("Resolve accepted an invalid token_store")
			}
			msg := err.Error()
			if !strings.Contains(msg, c.wantSrc) || !strings.Contains(msg, `"keychain"`) || !strings.Contains(msg, `"file"`) {
				t.Errorf("error %q should name %s and the allowed values", msg, c.wantSrc)
			}
		})
	}
}

func TestKeyHintByMode(t *testing.T) {
	if h := Radarr.KeyHint(TokenStoreKeychain); !strings.Contains(h, "security add-generic-password -s radarrctl") {
		t.Errorf("keychain hint = %q", h)
	}
	h := Sonarr.KeyHint(TokenStoreFile)
	if strings.Contains(h, "security") || !strings.Contains(h, "ARRCTL_SONARR_APIKEY") || !strings.Contains(h, "[sonarr]") {
		t.Errorf("file hint = %q", h)
	}
}

func TestWriteFileKeepsTokenStore(t *testing.T) {
	path := writeConfig(t, "")
	f := File{}
	f.Radarr.URL = "http://r.example"
	f.Defaults.TokenStore = TokenStoreFile
	if err := WriteFile(path, f, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	l, err := LoadFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := ResolveTokenStore(l); m != TokenStoreFile {
		t.Errorf("mode after write = %q, want file", m)
	}
}

func TestResolveRecordsKeychainTimeout(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	orig := keyLookup
	keyLookup = func(string, string) (string, error) {
		return "", fmt.Errorf("%w: timed out after 5s", keychain.ErrUnavailable)
	}
	t.Cleanup(func() { keyLookup = orig })
	l, err := LoadFile(writeConfig(t, sample), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeychainErr != "keychain unavailable: timed out after 5s" {
		t.Errorf("KeychainErr = %q", c.KeychainErr)
	}
}

func TestResolveNotFoundIsSilent(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	stubKeychain(t, nil)
	l, err := LoadFile(writeConfig(t, sample), false)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeychainErr != "" {
		t.Errorf("KeychainErr = %q, want silent on not-found", c.KeychainErr)
	}
}

func TestResolveNoURLSkipsKeychain(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	t.Setenv("ARRCTL_RADARR_URL", "")
	t.Setenv("ARRCTL_TIMEOUT", "")
	calls := 0
	orig := keyLookup
	keyLookup = func(string, string) (string, error) {
		calls++
		return "", fmt.Errorf("%w: timed out after 5s", keychain.ErrUnavailable)
	}
	t.Cleanup(func() { keyLookup = orig })
	if _, err := Resolve(Radarr, Flags{}, &Loaded{}); err == nil || !strings.Contains(err.Error(), "no url") {
		t.Fatalf("Resolve err = %v, want a no-url error", err)
	}
	if calls != 0 {
		t.Errorf("keyLookup called %d times, want 0 when the app has no url", calls)
	}
}

func TestResolveOtherKeychainErrorIsLabelled(t *testing.T) {
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	orig := keyLookup
	keyLookup = func(string, string) (string, error) { return "", fmt.Errorf("exit status 51") }
	t.Cleanup(func() { keyLookup = orig })
	l, _ := LoadFile(writeConfig(t, sample), false)
	c, err := Resolve(Radarr, Flags{}, l)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeychainErr != "keychain error: exit status 51" {
		t.Errorf("KeychainErr = %q", c.KeychainErr)
	}
}
