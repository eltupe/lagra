// Package secrets resolves sensitive backend options from their secret
// sources: the macOS keychain or the output of a command. Values are never
// logged and never written anywhere but the keychain.
package secrets

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/eltupe/lagra/internal/config"
)

// Service is the keychain service name for every lagra item.
const Service = "lagra"

// Account returns the keychain account for one option of one lagra account.
func Account(account, option string) string { return account + "/" + option }

// Keychain is the subset of keychain behaviour lagra needs.
type Keychain interface {
	Get(ctx context.Context, service, account string) (string, error)
	// Exists reports whether an item is there without reading its secret.
	Exists(ctx context.Context, service, account string) (bool, error)
	Set(ctx context.Context, service, account, value string) error
	Delete(ctx context.Context, service, account string) error
}

// ErrNotFound is returned when a keychain item does not exist.
var ErrNotFound = errors.New("keychain item not found")

// Runner executes a command and returns its stdout. Tests substitute it.
type Runner func(ctx context.Context, stdin string, name string, args ...string) (stdout []byte, err error)

// ExecRunner runs real processes.
func ExecRunner(ctx context.Context, stdin string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.Bytes(), &ExitError{Cmd: name, Err: err, Stderr: msg}
	}
	return out.Bytes(), nil
}

// ExitError carries the command's stderr without ever including stdin.
type ExitError struct {
	Cmd    string
	Err    error
	Stderr string
}

func (e *ExitError) Error() string { return fmt.Sprintf("%s: %s", e.Cmd, e.Stderr) }
func (e *ExitError) Unwrap() error { return e.Err }

// SecurityKeychain talks to the login keychain through /usr/bin/security.
// Writes go through `security -i` on stdin so the secret never shows up in a
// process list.
type SecurityKeychain struct {
	Run Runner
}

func (k SecurityKeychain) run() Runner {
	if k.Run == nil {
		return ExecRunner
	}
	return k.Run
}

// Get implements Keychain.
func (k SecurityKeychain) Get(ctx context.Context, service, account string) (string, error) {
	out, err := k.run()(ctx, "", "/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		var ee *ExitError
		if errors.As(err, &ee) && strings.Contains(ee.Stderr, "could not be found") {
			return "", fmt.Errorf("%w: service %q account %q", ErrNotFound, service, account)
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\r\n"), nil
}

// Exists implements Keychain. Without -w, security reads only the item's
// attributes: no secret leaves the keychain and no access prompt appears.
func (k SecurityKeychain) Exists(ctx context.Context, service, account string) (bool, error) {
	_, err := k.run()(ctx, "", "/usr/bin/security", "find-generic-password", "-s", service, "-a", account)
	var ee *ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && strings.Contains(ee.Stderr, "could not be found"):
		return false, nil
	}
	return false, err
}

// Set implements Keychain. -U updates an existing item in place.
func (k SecurityKeychain) Set(ctx context.Context, service, account, value string) error {
	script := fmt.Sprintf("add-generic-password -U -s %q -a %q -X %s\n", service, account, hex.EncodeToString([]byte(value)))
	_, err := k.run()(ctx, script, "/usr/bin/security", "-i")
	return err
}

// Delete implements Keychain.
func (k SecurityKeychain) Delete(ctx context.Context, service, account string) error {
	_, err := k.run()(ctx, "", "/usr/bin/security", "delete-generic-password", "-s", service, "-a", account)
	return err
}

// Resolver turns secret sources into values.
type Resolver struct {
	Keychain Keychain
	Run      Runner
}

// MissingError reports a keychain item that does not exist yet. Its text is
// short enough for a notification banner and says how to fix it.
type MissingError struct {
	Account, Option string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("account %q has no %s in the keychain; set it under Accounts or run: lagra secret set %s %s", e.Account, e.Option, e.Account, e.Option)
}

// Unwrap makes errors.Is(err, ErrNotFound) hold.
func (e *MissingError) Unwrap() error { return ErrNotFound }

// Resolve returns option name → value for every secret source of an account.
// It stops at the first failure so that a missing secret is reported
// precisely.
func (r *Resolver) Resolve(ctx context.Context, account string, sources map[string]config.SecretSource) (map[string]string, error) {
	out := make(map[string]string, len(sources))
	for _, option := range slices.Sorted(maps.Keys(sources)) {
		v, err := r.one(ctx, account, option, sources[option])
		if err != nil {
			var missing *MissingError
			if errors.As(err, &missing) {
				return nil, err
			}
			return nil, fmt.Errorf("account %q, secret %s: %w", account, option, err)
		}
		out[option] = v
	}
	return out, nil
}

func (r *Resolver) one(ctx context.Context, account, option string, src config.SecretSource) (string, error) {
	switch src.Kind() {
	case "keychain":
		if r.Keychain == nil {
			return "", errors.New("no keychain available")
		}
		v, err := r.Keychain.Get(ctx, Service, Account(account, option))
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return "", &MissingError{Account: account, Option: option}
			}
			return "", err
		}
		return v, nil
	case "command":
		run := r.Run
		if run == nil {
			run = ExecRunner
		}
		cctx, cancel := context.WithTimeout(ctx, src.SecretTimeout())
		defer cancel()
		out, err := run(cctx, "", src.Command[0], src.Command[1:]...)
		if err != nil {
			if errors.Is(cctx.Err(), context.DeadlineExceeded) {
				return "", fmt.Errorf("command %q timed out after %s", src.Command[0], src.SecretTimeout())
			}
			return "", err
		}
		v := strings.TrimRight(string(out), "\r\n")
		if v == "" {
			return "", fmt.Errorf("command %q produced no output", src.Command[0])
		}
		return v, nil
	}
	return "", errors.New("invalid secret source")
}

// Timeout is the default deadline for keychain operations.
const Timeout = 15 * time.Second
