package commands

import (
	"os"
	"testing"

	keyring "github.com/zalando/go-keyring"
)

// TestMain swaps in go-keyring's in-memory mock, so no command test ever reads
// the real Keychain (config show and config path both resolve keys).
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}
