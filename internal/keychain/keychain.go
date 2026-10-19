// Package keychain is the suite's only door to the OS secret store (macOS
// Keychain, or secret-service over D-Bus on Linux), via go-keyring. Every call
// is bounded.
//
// The bound exists because go-keyring on Linux talks to secret-service over the
// session bus, and an account with no session bus can hang there for ever. Each
// call runs in its own goroutine and is raced against a timer; when the timer
// wins the call returns ErrUnavailable and the goroutine is abandoned. That is
// acceptable for a short-lived CLI: the process exits soon after, and the
// goroutine with it.
//
// An abandoned Set can still land after its timeout. Callers must tolerate a
// late or stale secret-store copy; traktctl's freshest-created_at-wins load does.
package keychain

import (
	"errors"
	"fmt"
	"time"

	keyring "github.com/zalando/go-keyring"
)

// DefaultTimeout bounds every call, on every OS.
const DefaultTimeout = 5 * time.Second

// ErrNotFound is go-keyring's own sentinel, so errors.Is checks keep working
// without importing go-keyring.
var ErrNotFound = keyring.ErrNotFound

// ErrUnavailable is returned, wrapped, when a call does not finish in time.
var ErrUnavailable = errors.New("keychain unavailable")

// Seams for tests: the underlying calls and the bound.
var (
	getFn    = keyring.Get
	setFn    = keyring.Set
	deleteFn = keyring.Delete
	timeout  = DefaultTimeout
)

type result struct {
	val string
	err error
}

// bounded runs fn under the bound. The caller reads the seams and passes fn in,
// so the goroutine never touches a package variable.
func bounded(d time.Duration, fn func() (string, error)) (string, error) {
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.val, r.err
	case <-t.C:
		return "", fmt.Errorf("%w: timed out after %s", ErrUnavailable, d)
	}
}

// Get reads a secret.
func Get(service, user string) (string, error) {
	get := getFn
	return bounded(timeout, func() (string, error) { return get(service, user) })
}

// Set writes a secret.
func Set(service, user, secret string) error {
	set := setFn
	_, err := bounded(timeout, func() (string, error) { return "", set(service, user, secret) })
	return err
}

// Delete removes a secret.
func Delete(service, user string) error {
	del := deleteFn
	_, err := bounded(timeout, func() (string, error) { return "", del(service, user) })
	return err
}
