package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func loggedInAs(w *world, uuid, accessToken string) {
	w.goLive(uuid, accessToken, accessToken+"-refresh", inOneHour())
	w.setUsage(accessToken, uuid, 1.0, 1.0)
}

func TestAddSavesTheLiveLoginWithoutMCPLogins(t *testing.T) {
	w := newWorld(t)
	loggedInAs(w, "uuid-a", "token-a")

	w.must(addAccount("work@example.test"))

	if got := w.savedNames(); !reflect.DeepEqual(got, []string{"work@example.test"}) {
		t.Fatalf("saved names = %v", got)
	}
	if got := w.storedLogin("work@example.test").RefreshToken; got != "token-a-refresh" {
		t.Fatalf("stored refresh token = %q", got)
	}

	stored, err := loadStoredAccount("work@example.test")
	w.must(err)
	if _, found := stored.Credentials[mcpOAuthKey]; found {
		t.Fatal("MCP logins were saved with the account")
	}
}

func TestAddRefusesASecondNameForTheSameLogin(t *testing.T) {
	w := newWorld(t)
	loggedInAs(w, "uuid-a", "token-a")
	w.must(addAccount("first"))

	err := addAccount("second")

	if err == nil || !strings.Contains(err.Error(), "already saved as first") {
		t.Fatalf("error = %v", err)
	}
	if _, loadErr := loadStoredAccount("second"); !errors.Is(loadErr, errKeychainItemNotFound) {
		t.Fatalf("refused add still stored credentials: %v", loadErr)
	}
}

func TestAddRefusesANameUsedByAnotherAccount(t *testing.T) {
	w := newWorld(t)
	loggedInAs(w, "uuid-a", "token-a")
	w.must(addAccount("work"))
	loggedInAs(w, "uuid-b", "token-b")

	err := addAccount("work")

	if err == nil || !strings.Contains(err.Error(), "different account") {
		t.Fatalf("error = %v", err)
	}
	if got := w.storedLogin("work").RefreshToken; got != "token-a-refresh" {
		t.Fatalf("refused add replaced tokens: %q", got)
	}
}

func TestAddAgainUpdatesTokensWithoutDuplicating(t *testing.T) {
	w := newWorld(t)
	loggedInAs(w, "uuid-a", "token-a")
	w.must(addAccount("work"))
	loggedInAs(w, "uuid-a", "token-a-relogin")

	w.must(addAccount("work"))

	if got := w.savedNames(); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatalf("saved names = %v", got)
	}
	if got := w.storedLogin("work").RefreshToken; got != "token-a-relogin-refresh" {
		t.Fatalf("stored refresh token = %q", got)
	}
}

func TestAddRefusesWhenCachedIdentityIsAnotherAccount(t *testing.T) {
	w := newWorld(t)
	loggedInAs(w, "uuid-a", "token-a")
	w.setUsage("token-a", "uuid-after-login", 1.0, 1.0)

	if err := addAccount("work"); err == nil {
		t.Fatal("saved a login under another account's identity")
	}
	if got := w.savedNames(); len(got) != 0 {
		t.Fatalf("saved names = %v", got)
	}
}

func TestAddRejectsNamesOutsideTheAllowedCharacters(t *testing.T) {
	w := newWorld(t)
	loggedInAs(w, "uuid-a", "token-a")

	for _, name := range []string{"", "has space", `quo"te`, "../escape"} {
		if err := addAccount(name); err == nil {
			t.Fatalf("name %q was accepted", name)
		}
	}
}

func TestRemoveDeletesTheAccountAndItsKeychainItem(t *testing.T) {
	w := newWorld(t)
	w.saveAccount("keep", "uuid-keep", "keep", "keep", inOneHour())
	w.saveAccount("drop", "uuid-drop", "drop", "drop", inOneHour())

	w.must(removeAccount("drop"))

	if got := w.savedNames(); !reflect.DeepEqual(got, []string{"keep"}) {
		t.Fatalf("saved names = %v", got)
	}
	if _, err := loadStoredAccount("drop"); !errors.Is(err, errKeychainItemNotFound) {
		t.Fatalf("keychain item survived: %v", err)
	}
}

func TestRemoveUnknownAccountFails(t *testing.T) {
	newWorld(t)

	if err := removeAccount("nobody"); err == nil {
		t.Fatal("removing an unknown account succeeded")
	}
}

// These stay on rejected input: a successful turnOn would call the real launchctl.
func TestOnRejectsInvalidRequestsBeforeInstallingTheAgent(t *testing.T) {
	cases := map[string][]string{
		"5h above 100":     {"--5h", "101"},
		"7d zero":          {"--7d", "0"},
		"too few accounts": {},
		"stray argument":   {"extra"},
	}

	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.saveAccount("only", "uuid-only", "only", "only", inOneHour())

			if err := turnOn(args); err == nil {
				t.Fatal("invalid request was accepted")
			}

			plist := filepath.Join(w.home, "Library", "LaunchAgents", agentLabel+".plist")
			if _, err := os.Stat(plist); !os.IsNotExist(err) {
				t.Fatal("an agent was installed")
			}
		})
	}
}

func TestKeychainRoundTripsValuesLongerThanTheStdinLimit(t *testing.T) {
	w := newWorld(t)
	longValue := bytes.Repeat([]byte("x"), securityStdinLineLimit)

	w.must(keychainWrite("service with space", "person", longValue))

	stored, err := keychainRead("service with space", "person")
	w.must(err)
	if !bytes.Equal(stored, longValue) {
		t.Fatalf("stored %d bytes, want %d", len(stored), len(longValue))
	}
}

func TestKeychainWriteFailsWhenReadBackDiffers(t *testing.T) {
	w := newWorld(t)
	w.must(os.WriteFile(filepath.Join(w.keychainDir, "corrupt-next-live-write"), nil, 0o600))

	if err := keychainWrite(claudeKeychainService, "person", []byte("{}")); err == nil {
		t.Fatal("a corrupted write was reported as success")
	}
}

func TestKeychainDeleteReportsAMissingItem(t *testing.T) {
	newWorld(t)

	if err := keychainDelete("service", "absent"); !errors.Is(err, errKeychainItemNotFound) {
		t.Fatalf("error = %v", err)
	}
}
