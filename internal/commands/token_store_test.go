package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corinthian/arrctl/internal/keychain"
	keyring "github.com/zalando/go-keyring"
)

func TestConfigPathReportsTokenStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
	t.Setenv("ARRCTL_TOKEN_STORE", "file")
	out, cerr := run(t, "config", "path")
	if cerr != nil {
		t.Fatalf("config path: %v", cerr)
	}
	var env struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if env.Data["token_store"] != "file" {
		t.Errorf("token_store = %v, want file\n%s", env.Data["token_store"], out)
	}
	keys, _ := env.Data["apikeys"].(map[string]interface{})
	radarr, _ := keys["radarr"].(map[string]interface{})
	if radarr["service"] != nil {
		t.Errorf("file mode still reports a Keychain service: %v", radarr)
	}
}

func TestConfigShowReportsTokenStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
	t.Setenv("ARRCTL_TOKEN_STORE", "file")
	t.Setenv("ARRCTL_RADARR_URL", "http://r.example")
	out, cerr := run(t, "config", "show")
	if cerr != nil {
		t.Fatalf("config show: %v", cerr)
	}
	var env struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if env.Data["token_store"] != "file" {
		t.Errorf("token_store = %v, want file\n%s", env.Data["token_store"], out)
	}
}

// TestMissingKeyErrorNamesKeychainTimeout: end to end, a timed-out Keychain
// read reaches the missing-key error text; a plain not-found does not.
func TestMissingKeyErrorNamesKeychainTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_URL", "http://127.0.0.1:1")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")

	_, cerr := run(t, "radarr", "queue", "list")
	if cerr == nil {
		t.Fatal("expected a missing-key error")
	}
	if strings.Contains(cerr.Message, "keychain") {
		t.Errorf("not-found should stay silent, got %q", cerr.Message)
	}

	keyring.MockInitWithError(fmt.Errorf("%w: timed out after 5s", keychain.ErrUnavailable))
	t.Cleanup(keyring.MockInit)
	_, cerr = run(t, "radarr", "queue", "list")
	if cerr == nil || !strings.Contains(cerr.Message, "keychain unavailable: timed out after 5s") {
		t.Fatalf("cerr = %+v, want the keychain timeout in the message", cerr)
	}
}

func TestConfigShowAndPathReportKeychainError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	t.Setenv("ARRCTL_RADARR_URL", "http://r.example")
	t.Setenv("ARRCTL_RADARR_APIKEY", "")
	keyring.MockInitWithError(fmt.Errorf("%w: timed out after 5s", keychain.ErrUnavailable))
	t.Cleanup(keyring.MockInit)

	for _, cmd := range []string{"show", "path"} {
		out, cerr := run(t, "config", cmd)
		if cerr != nil {
			t.Fatalf("config %s: %v", cmd, cerr)
		}
		if !strings.Contains(out, `"keychain_error": "keychain unavailable: timed out after 5s"`) {
			t.Errorf("config %s does not report the keychain timeout:\n%s", cmd, out)
		}
	}
}

// tokenStoreData runs a diagnostic against a config body and returns its data
// and its stderr, kept apart so a warning cannot hide inside the payload.
func tokenStoreData(t *testing.T, body string, args ...string) (map[string]interface{}, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARRCTL_CONFIG", "")
	t.Setenv("ARRCTL_TOKEN_STORE", "")
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	root, app := NewRoot()
	var stdout, stderr bytes.Buffer
	app.Out.Out = &stdout
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append(args, "--config", p))
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	var env struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	return env.Data, stderr.String()
}

func TestTokenStoreStatus(t *testing.T) {
	const apps = "[radarr]\nurl = \"http://r.example\"\n"
	cases := []struct {
		name, body, status, mode string
		hint, errKey             bool
	}{
		{"unset", apps, "valid", "keychain", false, false},
		{"defaults", apps + "[defaults]\ntoken_store = \"file\"\n", "valid", "file", false, false},
		{"top level", "token_store = \"file\"\n" + apps, "misplaced", "keychain", true, false},
		{"top level and defaults", "token_store = \"keychain\"\n" + apps + "[defaults]\ntoken_store = \"file\"\n", "misplaced", "file", true, false},
		{"bad value", apps + "[defaults]\ntoken_store = \"vault\"\n", "invalid", "", false, true},
		{"top level and bad value", "token_store = \"file\"\n" + apps + "[defaults]\ntoken_store = \"vault\"\n", "invalid", "", false, true},
	}
	for _, cmd := range []string{"path", "show"} {
		for _, c := range cases {
			t.Run(cmd+"/"+c.name, func(t *testing.T) {
				d, stderr := tokenStoreData(t, c.body, "config", cmd)
				if warned := strings.Contains(stderr, "[defaults]"); warned != strings.HasPrefix(c.name, "top level") {
					t.Errorf("stderr warning = %v for %q: %q", warned, c.name, stderr)
				}
				if d["token_store_status"] != c.status {
					t.Errorf("token_store_status = %v, want %s", d["token_store_status"], c.status)
				}
				if c.mode == "" {
					if _, ok := d["token_store"]; ok {
						t.Errorf("token_store = %v, want absent", d["token_store"])
					}
				} else if d["token_store"] != c.mode {
					t.Errorf("token_store = %v, want %s", d["token_store"], c.mode)
				}
				hint, _ := d["token_store_hint"].(string)
				if c.hint != strings.Contains(hint, "[defaults]") {
					t.Errorf("token_store_hint = %q, want naming [defaults]: %v", hint, c.hint)
				}
				if _, ok := d["token_store_error"]; ok != c.errKey {
					t.Errorf("token_store_error present = %v, want %v", ok, c.errKey)
				}
			})
		}
	}
}
