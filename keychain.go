package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// An absolute path keeps a credential tool from running whatever "security" is
// first on PATH. Using Apple's binary also means one Keychain "Always Allow"
// grant survives every rebuild of ccrotate.
const securityBin = "/usr/bin/security"

// errSecItemNotFound as reported by security(1).
const securityExitItemNotFound = 44

const securityTimeout = 10 * time.Second

// security -i reads commands into a 4096-byte buffer and silently truncates
// longer lines, which would store cut-off JSON. Measured by clauth
// (src/keychain.rs, SECURITY_STDIN_LINE_MAX); the margin is claude-swap's.
const securityStdinLineLimit = 4096 - 64

var errKeychainItemNotFound = errors.New("keychain item not found")

func keychainRead(service, account string) ([]byte, error) {
	output, err := runSecurity(nil, "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return nil, fmt.Errorf("read keychain item %q: %w", service, err)
	}

	return bytes.TrimSuffix(output, []byte("\n")), nil
}

// keychainWrite creates or replaces an item and reads it back, because
// security can exit 0 after storing truncated bytes.
func keychainWrite(service, account string, value []byte) error {
	hexValue := hex.EncodeToString(value)
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -X %s\n",
		securityQuote(service), securityQuote(account), hexValue)

	var err error
	if len(command) <= securityStdinLineLimit {
		// stdin keeps the secret out of the process argument list.
		_, err = runSecurity(strings.NewReader(command), "-i")
	} else {
		_, err = runSecurity(nil, "add-generic-password", "-U", "-s", service, "-a", account, "-X", hexValue)
	}
	if err != nil {
		return fmt.Errorf("write keychain item %q: %w", service, err)
	}

	stored, err := keychainRead(service, account)
	if err != nil {
		return fmt.Errorf("verify keychain item %q: %w", service, err)
	}

	if !bytes.Equal(stored, value) {
		return fmt.Errorf("verify keychain item %q: stored bytes differ from the bytes written", service)
	}

	return nil
}

func keychainDelete(service, account string) error {
	_, err := runSecurity(nil, "delete-generic-password", "-s", service, "-a", account)
	if err != nil {
		return fmt.Errorf("delete keychain item %q: %w", service, err)
	}

	return nil
}

func securityQuote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)

	return `"` + escaped + `"`
}

func runSecurity(stdin *strings.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, securityBin, args...)
	if stdin != nil {
		command.Stdin = stdin
	}

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == securityExitItemNotFound {
		return nil, errKeychainItemNotFound
	}

	return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
}
