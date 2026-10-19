package keychain

import (
	"errors"
	"testing"
	"time"
)

// seam swaps the underlying calls and the bound for one test. A hanging fake
// is released in Cleanup so no goroutine outlives the test.
func seam(t *testing.T, d time.Duration, get func(string, string) (string, error),
	set func(string, string, string) error, del func(string, string) error) {
	t.Helper()
	og, oset, od, ot := getFn, setFn, deleteFn, timeout
	getFn, setFn, deleteFn, timeout = get, set, del, d
	t.Cleanup(func() { getFn, setFn, deleteFn, timeout = og, oset, od, ot })
}

func hang(t *testing.T) chan struct{} {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return release
}

func TestDefaultTimeoutIsFiveSeconds(t *testing.T) {
	if DefaultTimeout != 5*time.Second {
		t.Fatalf("DefaultTimeout = %s, want 5s", DefaultTimeout)
	}
	if timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want DefaultTimeout", timeout)
	}
}

func TestHangingCallsReturnUnavailableWithinBound(t *testing.T) {
	release := hang(t)
	const bound = 50 * time.Millisecond
	seam(t, bound,
		func(string, string) (string, error) { <-release; return "late", nil },
		func(string, string, string) error { <-release; return nil },
		func(string, string) error { <-release; return nil },
	)
	check := func(name string, call func() error) {
		start := time.Now()
		err := call()
		if el := time.Since(start); el > bound+500*time.Millisecond {
			t.Errorf("%s took %s, bound %s", name, el, bound)
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s err = %v, want ErrUnavailable", name, err)
		}
	}
	check("Get", func() error {
		v, err := Get("s", "u")
		if v != "" {
			t.Errorf("Get value = %q after timeout, want empty", v)
		}
		return err
	})
	check("Set", func() error { return Set("s", "u", "x") })
	check("Delete", func() error { return Delete("s", "u") })
}

func TestFastCallsPassThrough(t *testing.T) {
	boom := errors.New("boom")
	var gotSecret string
	seam(t, time.Second,
		func(s, u string) (string, error) {
			if s == "missing" {
				return "", ErrNotFound
			}
			return s + "/" + u, nil
		},
		func(s, u, p string) error { gotSecret = p; return nil },
		func(s, u string) error { return boom },
	)
	if v, err := Get("svc", "usr"); err != nil || v != "svc/usr" {
		t.Errorf("Get = %q, %v", v, err)
	}
	if _, err := Get("missing", "usr"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing err = %v, want ErrNotFound", err)
	}
	if err := Set("s", "u", "secret"); err != nil || gotSecret != "secret" {
		t.Errorf("Set err = %v, stored %q", err, gotSecret)
	}
	if err := Delete("s", "u"); !errors.Is(err, boom) || errors.Is(err, ErrUnavailable) {
		t.Errorf("Delete err = %v, want boom untouched", err)
	}
}
