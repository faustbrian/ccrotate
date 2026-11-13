package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// threeAccounts leaves "a" live and over the limit, with stored tokens older
// than the live ones, and "b" and "c" saved.
func threeAccounts(t *testing.T) *world {
	t.Helper()

	w := newWorld(t)
	w.saveAccount("a", "uuid-a", "a-stored-access", "a-stored-refresh", inOneHour())
	w.saveAccount("b", "uuid-b", "b-access", "b-refresh", inOneHour())
	w.saveAccount("c", "uuid-c", "c-access", "c-refresh", inOneHour())
	w.goLive("uuid-a", "a-live-access", "a-live-refresh", inOneHour())
	w.setUsage("a-live-access", "uuid-a", 0.0, 98.0)
	w.setUsage("b-access", "uuid-b", 10.0, 10.0)
	w.setUsage("c-access", "uuid-c", 20.0, 20.0)
	return w
}

func assertLiveAccess(t *testing.T, w *world, want string) {
	t.Helper()
	if got := w.liveLogin().AccessToken; got != want {
		t.Fatalf("live access token = %q, want %q", got, want)
	}
}

func TestRotationInstallsNextAccountAtExactThreshold(t *testing.T) {
	w := threeAccounts(t)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "b-access")
	if got := w.cachedIdentityUUID(); got != "uuid-b" {
		t.Fatalf("~/.claude.json identity = %q, want uuid-b", got)
	}
}

func TestRotationSavesOutgoingAccountsNewestTokens(t *testing.T) {
	w := threeAccounts(t)

	w.must(rotateIfNeeded())

	if got := w.storedLogin("a").RefreshToken; got != "a-live-refresh" {
		t.Fatalf("stored refresh token for a = %q, want the live one", got)
	}
}

func TestRotationKeepsMCPLoginsOnTheLiveItemOnly(t *testing.T) {
	w := threeAccounts(t)

	w.must(rotateIfNeeded())

	live, err := readLiveCredentials()
	w.must(err)
	if !strings.Contains(string(live[mcpOAuthKey]), "mcp-token") {
		t.Fatalf("live item lost its MCP logins: %s", live[mcpOAuthKey])
	}

	stored, err := loadStoredAccount("a")
	w.must(err)
	if _, found := stored.Credentials[mcpOAuthKey]; found {
		t.Fatal("MCP logins were copied into a saved account")
	}
}

func TestRotationPreservesTheRestOfClaudeJSON(t *testing.T) {
	w := threeAccounts(t)

	w.must(rotateIfNeeded())

	raw, err := os.ReadFile(filepath.Join(w.home, ".claude.json"))
	w.must(err)
	if !strings.Contains(string(raw), `"keep": "<me>"`) {
		t.Fatalf("~/.claude.json lost or re-escaped other keys: %s", raw)
	}
}

func TestRotationReleasesClaudeLocks(t *testing.T) {
	w := threeAccounts(t)

	w.must(rotateIfNeeded())

	dirs, err := claudeLockDirs()
	w.must(err)
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("lock %s was left behind", dir)
		}
	}
}

func TestNoRotationBelowThreshold(t *testing.T) {
	w := threeAccounts(t)
	w.setUsage("a-live-access", "uuid-a", 97.9, 97.9)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "a-live-access")
}

func TestRotationSkipsAccountsAtLimitOrUnreadable(t *testing.T) {
	w := threeAccounts(t)
	w.setUsage("b-access", "uuid-b", 99.0, 10.0)
	delete(w.api.byToken, "c-access")
	w.saveAccount("d", "uuid-d", "d-access", "d-refresh", inOneHour())
	w.setUsage("d-access", "uuid-d", 1.0, 1.0)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "d-access")
}

func TestRotationWrapsPastTheLastAccount(t *testing.T) {
	w := threeAccounts(t)
	w.goLive("uuid-c", "c-live-access", "c-live-refresh", inOneHour())
	w.setUsage("c-live-access", "uuid-c", 100.0, 1.0)
	w.setUsage("a-stored-access", "uuid-a", 1.0, 1.0)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "a-stored-access")
}

func TestStaysWhenNoAccountHasRoom(t *testing.T) {
	w := threeAccounts(t)
	w.setUsage("b-access", "uuid-b", 98.0, 98.0)
	w.setUsage("c-access", "uuid-c", 100.0, 100.0)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "a-live-access")
}

func TestNoRotationWhenLiveLoginIsNotSaved(t *testing.T) {
	w := threeAccounts(t)
	w.goLive("uuid-stranger", "stranger-access", "stranger-refresh", inOneHour())
	w.setUsage("stranger-access", "uuid-stranger", 100.0, 100.0)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "stranger-access")
	if got := w.storedLogin("a").RefreshToken; got != "a-stored-refresh" {
		t.Fatalf("a stranger's login overwrote account a: %q", got)
	}
}

func TestHoldsWithoutNetworkWhenLiveTokenExpired(t *testing.T) {
	w := threeAccounts(t)
	w.goLive("uuid-a", "a-live-access", "a-live-refresh", time.Now().Add(-time.Minute).UnixMilli())

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "a-live-access")
	if len(w.api.requests) != 0 {
		t.Fatalf("expired live token still caused requests: %v", w.api.requests)
	}
}

func TestExpiringCandidateIsRefreshedAndInstalled(t *testing.T) {
	w := threeAccounts(t)
	w.saveAccount("soon", "uuid-soon", "old-access", "old-refresh", time.Now().Add(time.Minute).UnixMilli())
	w.setUsage("b-access", "uuid-b", 100.0, 100.0)
	w.setUsage("c-access", "uuid-c", 100.0, 100.0)
	w.api.refreshes["old-refresh"] = tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 28800}
	w.setUsage("new-access", "uuid-soon", 1.0, 1.0)

	w.must(rotateIfNeeded())

	login := w.liveLogin()
	if login.AccessToken != "new-access" || login.RefreshToken != "new-refresh" {
		t.Fatalf("live login = %q/%q, want the refreshed pair", login.AccessToken, login.RefreshToken)
	}
	if !login.expiresWithin(9*time.Hour) || login.expiresWithin(7*time.Hour) {
		t.Fatalf("expiry %d does not reflect expires_in of 8 hours", login.ExpiresAt)
	}

	live, err := readLiveCredentials()
	w.must(err)
	if !strings.Contains(string(live[claudeLoginKey]), `"subscriptionType":"max"`) {
		t.Fatalf("refresh dropped other login fields: %s", live[claudeLoginKey])
	}
}

func TestRefreshedTokensPersistWhenAccountIsThenSkipped(t *testing.T) {
	w := threeAccounts(t)
	w.saveAccount("full", "uuid-full", "old-access", "old-refresh", time.Now().Add(time.Minute).UnixMilli())
	w.setUsage("b-access", "uuid-b", 100.0, 100.0)
	w.setUsage("c-access", "uuid-c", 100.0, 100.0)
	w.api.refreshes["old-refresh"] = tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 28800}
	w.setUsage("new-access", "uuid-full", 100.0, 100.0)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "a-live-access")
	if got := w.storedLogin("full").RefreshToken; got != "new-refresh" {
		t.Fatalf("spent refresh token kept after refresh: %q", got)
	}
}

func TestRejectedRefreshSkipsAccountAndKeepsItsTokens(t *testing.T) {
	w := threeAccounts(t)
	w.saveAccount("dead", "uuid-dead", "old-access", "dead-refresh", time.Now().Add(-time.Hour).UnixMilli())
	w.setUsage("b-access", "uuid-b", 100.0, 100.0)
	w.setUsage("c-access", "uuid-c", 100.0, 100.0)

	w.must(rotateIfNeeded())

	assertLiveAccess(t, w, "a-live-access")
	if got := w.storedLogin("dead").RefreshToken; got != "dead-refresh" {
		t.Fatalf("rejected refresh changed stored token: %q", got)
	}
}

func nextAccountB(t *testing.T) storedAccount {
	t.Helper()
	return storedAccount{Credentials: loginCredentials(t, "b-access", "b-refresh", inOneHour()), OAuthAccount: identityFor("uuid-b")}
}

func TestSwitchAbortsWhenLiveLoginChangedAfterTheCheck(t *testing.T) {
	w := threeAccounts(t)

	err := switchLogin("a", nextAccountB(t), "token-from-the-earlier-check")

	if err == nil {
		t.Fatal("switch went ahead on a login that changed")
	}
	assertLiveAccess(t, w, "a-live-access")
	if got := w.storedLogin("a").RefreshToken; got != "a-stored-refresh" {
		t.Fatalf("aborted switch overwrote account a: %q", got)
	}
}

func TestSwitchAbortsAndNeverStealsAClaudeCodeLock(t *testing.T) {
	w := threeAccounts(t)
	heldLock := filepath.Join(w.home, ".claude.lock")
	w.must(os.Mkdir(heldLock, 0o700))
	longAgo := time.Now().Add(-time.Hour)
	w.must(os.Chtimes(heldLock, longAgo, longAgo))

	err := switchLogin("a", nextAccountB(t), "a-live-access")

	if err == nil {
		t.Fatal("switch went ahead while Claude Code held a lock")
	}
	assertLiveAccess(t, w, "a-live-access")
	if _, statErr := os.Stat(heldLock); statErr != nil {
		t.Fatal("the held lock was removed")
	}
	if _, statErr := os.Stat(filepath.Join(w.home, ".claude", ".oauth_refresh.lock")); !os.IsNotExist(statErr) {
		t.Fatal("the lock acquired before the contended one was not released")
	}
}

func TestSwitchRestoresLoginWhenKeychainWriteIsCorrupted(t *testing.T) {
	w := threeAccounts(t)
	w.must(os.WriteFile(filepath.Join(w.keychainDir, "corrupt-next-live-write"), nil, 0o600))

	err := switchLogin("a", nextAccountB(t), "a-live-access")

	if err == nil {
		t.Fatal("corrupted write was reported as success")
	}
	assertLiveAccess(t, w, "a-live-access")
	if got := w.cachedIdentityUUID(); got != "uuid-a" {
		t.Fatalf("identity moved despite failed switch: %q", got)
	}
}

func TestSwitchLeavesLoginAloneWhenClaudeJSONIsMalformed(t *testing.T) {
	w := threeAccounts(t)
	w.must(os.WriteFile(filepath.Join(w.home, ".claude.json"), []byte("{not json"), 0o600))

	err := switchLogin("a", nextAccountB(t), "a-live-access")

	if err == nil {
		t.Fatal("switch went ahead with an unreadable ~/.claude.json")
	}
	assertLiveAccess(t, w, "a-live-access")
}

func window(percent float64) *usageWindow {
	return &usageWindow{Utilization: &percent}
}

func TestThresholds(t *testing.T) {
	cfg := config{FiveHourThreshold: 90, SevenDayThreshold: 98}

	cases := []struct {
		name    string
		reading usageReading
		want    bool
	}{
		{"both below", usageReading{window(89.9), window(97.9)}, false},
		{"5h at its own limit", usageReading{window(90), window(1)}, true},
		{"7d below its own limit but above the 5h limit", usageReading{window(1), window(95)}, false},
		{"7d at its own limit", usageReading{window(1), window(98)}, true},
		{"5h window absent", usageReading{nil, window(98)}, true},
		{"utilization null", usageReading{&usageWindow{}, window(1)}, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.reading.reachesThresholds(cfg); got != testCase.want {
				t.Fatalf("reachesThresholds = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestFetchUsageRejectsResponseWithNoReadings(t *testing.T) {
	w := newWorld(t)
	w.setUsage("token", "uuid", nil, nil)

	if _, err := fetchUsage("token"); err == nil {
		t.Fatal("a usage response with no readings was accepted")
	}
}

func TestRefreshFailureReportsOnlyTheOAuthErrorCode(t *testing.T) {
	newWorld(t)

	_, err := refreshTokens(oauthLogin{RefreshToken: "unknown"})

	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error = %v, want invalid_grant", err)
	}
	if strings.Contains(err.Error(), "secret-detail") {
		t.Fatalf("response body reached the error: %v", err)
	}
}

func TestLoginRejectsCredentialsWithoutTokens(t *testing.T) {
	cases := map[string]credentialItem{
		"no login":         {},
		"malformed login":  {claudeLoginKey: json.RawMessage(`"nope"`)},
		"no refresh token": {claudeLoginKey: json.RawMessage(`{"accessToken":"a"}`)},
	}

	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := item.login(); err == nil {
				t.Fatal("invalid credentials were accepted")
			}
		})
	}
}

func TestUseInstallsChosenAccountEvenWhenItIsNotNext(t *testing.T) {
	w := threeAccounts(t)
	w.setUsage("a-live-access", "uuid-a", 0.0, 10.0)
	w.setUsage("c-access", "uuid-c", 99.0, 99.0)

	w.must(useAccount("c"))

	assertLiveAccess(t, w, "c-access")
	if got := w.cachedIdentityUUID(); got != "uuid-c" {
		t.Fatalf("~/.claude.json identity = %q, want uuid-c", got)
	}
	if got := w.storedLogin("a").RefreshToken; got != "a-live-refresh" {
		t.Fatalf("stored refresh token for a = %q, want the live one", got)
	}
}

func TestUseRejectsUnknownAccount(t *testing.T) {
	w := threeAccounts(t)

	if err := useAccount("missing"); err == nil {
		t.Fatal("use switched to an account that is not saved")
	}
	assertLiveAccess(t, w, "a-live-access")
}

func TestUseLeavesLiveAccountAloneWhenAlreadyLive(t *testing.T) {
	w := threeAccounts(t)

	w.must(useAccount("a"))

	assertLiveAccess(t, w, "a-live-access")
}
