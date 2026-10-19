package commands

import (
	"encoding/json"
	"fmt"
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
