package secrets

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eltupe/lagra/internal/config"
)

type call struct {
	stdin string
	name  string
	args  []string
}

func fakeRunner(calls *[]call, out string, err error) Runner {
	return func(ctx context.Context, stdin string, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{stdin, name, args})
		return []byte(out), err
	}
}

func TestKeychainGet(t *testing.T) {
	var calls []call
	k := SecurityKeychain{Run: fakeRunner(&calls, "s3cr3t\n", nil)}
	v, err := k.Get(context.Background(), Service, Account("archiv", "secret_access_key"))
	if err != nil || v != "s3cr3t" {
		t.Fatalf("got %q %v", v, err)
	}
	if strings.Join(calls[0].args, " ") != "find-generic-password -s lagra -a archiv/secret_access_key -w" {
		t.Errorf("args = %v", calls[0].args)
	}
}

func TestKeychainGetNotFound(t *testing.T) {
	var calls []call
	k := SecurityKeychain{Run: fakeRunner(&calls, "", &ExitError{Cmd: "security", Stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."})}
	_, err := k.Get(context.Background(), Service, "x/y")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestKeychainSetUsesStdinHex(t *testing.T) {
	var calls []call
	k := SecurityKeychain{Run: fakeRunner(&calls, "", nil)}
	if err := k.Set(context.Background(), Service, "archiv/access_key_id", "AKIA\"quote"); err != nil {
		t.Fatal(err)
	}
	c := calls[0]
	if strings.Join(c.args, " ") != "-i" {
		t.Errorf("args = %v", c.args)
	}
	if strings.Contains(c.stdin, "AKIA") {
		t.Errorf("secret must be hex encoded, got %q", c.stdin)
	}
	if !strings.Contains(c.stdin, "-X 414b494122") { // hex("AKIA\"")
		t.Errorf("stdin = %q", c.stdin)
	}
	for _, a := range c.args {
		if strings.Contains(a, "AKIA") {
			t.Error("secret leaked into argv")
		}
	}
}

func TestResolve(t *testing.T) {
	var calls []call
	r := &Resolver{
		Keychain: SecurityKeychain{Run: fakeRunner(&calls, "from-keychain", nil)},
		Run:      fakeRunner(&calls, "from-op\n", nil),
	}
	got, err := r.Resolve(context.Background(), "archiv", map[string]config.SecretSource{
		"access_key_id":     {Keychain: true},
		"secret_access_key": {Command: []string{"op", "read", "op://x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["access_key_id"] != "from-keychain" || got["secret_access_key"] != "from-op" {
		t.Errorf("got %v", got)
	}
}

func TestResolveMissingKeychainItemHints(t *testing.T) {
	var calls []call
	r := &Resolver{Keychain: SecurityKeychain{Run: fakeRunner(&calls, "", &ExitError{Cmd: "security", Stderr: "could not be found"})}}
	_, err := r.Resolve(context.Background(), "archiv", map[string]config.SecretSource{"access_key_id": {Keychain: true}})
	if err == nil || err.Error() != `account "archiv" has no access_key_id in the keychain; set it under Accounts or run: lagra secret set archiv access_key_id` {
		t.Fatalf("want hint, got %v", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Error("a missing item must still be ErrNotFound")
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0].args, " "), "-a archiv/access_key_id") {
		t.Errorf("looked up %v", calls)
	}
}

func TestResolveCommandTimeout(t *testing.T) {
	r := &Resolver{Run: func(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	to := config.Duration(20 * time.Millisecond)
	_, err := r.Resolve(context.Background(), "a", map[string]config.SecretSource{"pass": {Command: []string{"slow"}, Timeout: &to}})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestResolveEmptyOutput(t *testing.T) {
	var calls []call
	r := &Resolver{Run: fakeRunner(&calls, "\n", nil)}
	_, err := r.Resolve(context.Background(), "a", map[string]config.SecretSource{"pass": {Command: []string{"echo"}}})
	if err == nil || !strings.Contains(err.Error(), "no output") {
		t.Fatalf("got %v", err)
	}
}
