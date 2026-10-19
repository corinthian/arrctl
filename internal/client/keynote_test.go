package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/arrctl/internal/output"
)

func TestMissingKeyErrorCarriesKeychainNote(t *testing.T) {
	c := New(Config{App: "radarr", BaseURL: "http://127.0.0.1:1", Timeout: time.Second,
		Version: "test", KeyHint: "hint", KeyNote: "keychain unavailable: timed out after 5s"})
	_, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr == nil || cerr.Code != output.CodeBadConfig {
		t.Fatalf("cerr = %+v, want BAD_CONFIG", cerr)
	}
	if !strings.Contains(cerr.Message, "no api key for radarr") ||
		!strings.Contains(cerr.Message, "keychain unavailable: timed out after 5s") {
		t.Errorf("message = %q, want the missing-key text and the keychain timeout", cerr.Message)
	}
}

func TestMissingKeyErrorWithoutNoteIsUnchanged(t *testing.T) {
	c := New(Config{App: "radarr", BaseURL: "http://127.0.0.1:1", Timeout: time.Second,
		Version: "test", KeyHint: "hint"})
	_, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr == nil || cerr.Message != "no api key for radarr" {
		t.Fatalf("cerr = %+v, want the bare missing-key message", cerr)
	}
}

func TestRejectedKeyErrorCarriesKeychainNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := New(Config{App: "radarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second,
		Version: "test", KeyHint: "hint", KeyNote: "keychain unavailable: timed out after 5s"})
	_, cerr := c.Do(context.Background(), http.MethodGet, "/movie", Options{})
	if cerr == nil || cerr.Code != output.CodeArrAuthFailed {
		t.Fatalf("cerr = %+v, want ARR_AUTH_FAILED", cerr)
	}
	if !strings.Contains(cerr.Message, "keychain unavailable: timed out after 5s") || strings.Contains(cerr.Message, "k)") {
		t.Errorf("message = %q", cerr.Message)
	}
}
