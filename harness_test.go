package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeSecurityScript stands in for /usr/bin/security and stores items as files.
// Like the real tool, -i rejects a command line over its 4096-byte buffer.
const fakeSecurityScript = `#!/bin/sh
store="$FAKE_KEYCHAIN_DIR"
if [ "$1" = "-i" ]; then
  IFS= read -r line
  [ "${#line}" -le 4095 ] || exit 1
  eval "set -- $line"
fi
command="$1"; shift
service=""; account=""; hex=""
while [ $# -gt 0 ]; do
  case "$1" in
    -s) service="$2"; shift 2 ;;
    -a) account="$2"; shift 2 ;;
    -X) hex="$2"; shift 2 ;;
    *) shift ;;
  esac
done
file="$store/$(printf '%s|%s' "$service" "$account" | shasum | cut -d' ' -f1)"
case "$command" in
  find-generic-password) [ -f "$file" ] || exit 44; cat "$file"; echo ;;
  add-generic-password)
    if [ "$service" = "Claude Code-credentials" ] && [ -f "$store/corrupt-next-live-write" ]; then
      rm "$store/corrupt-next-live-write"
      printf 'truncated' > "$file"
      exit 0
    fi
    printf '%s' "$hex" | xxd -r -p > "$file" ;;
  delete-generic-password) [ -f "$file" ] || exit 44; rm "$file" ;;
esac
`

// TestMain makes it impossible for a test that skips newWorld to reach the real
// Keychain, home directory or network.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ccrotate-test-home")
	if err != nil {
		panic(err)
	}

	if err := os.Setenv("HOME", home); err != nil {
		panic(err)
	}

	securityBin = filepath.Join(home, "security-not-configured")
	httpClient = &http.Client{Transport: refuseNetwork{}}
	log.SetOutput(io.Discard)

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

type refuseNetwork struct{}

func (refuseNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("test made a network call without newWorld")
}

type apiAccount struct {
	uuid     string
	fiveHour any
	sevenDay any
}

// fakeAnthropic answers the usage, profile and token endpoints.
type fakeAnthropic struct {
	mu        sync.Mutex
	byToken   map[string]apiAccount
	refreshes map[string]tokenResponse
	requests  []string
}

func (api *fakeAnthropic) RoundTrip(request *http.Request) (*http.Response, error) {
	api.mu.Lock()
	defer api.mu.Unlock()

	api.requests = append(api.requests, request.URL.Path)

	if request.URL.Path == "/v1/oauth/token" {
		return api.answerRefresh(request)
	}

	token := request.Header.Get("Authorization")[len("Bearer "):]
	account, known := api.byToken[token]
	if !known {
		return respond(http.StatusUnauthorized, `{}`), nil
	}

	switch request.URL.Path {
	case "/api/oauth/usage":
		body, _ := json.Marshal(map[string]any{
			"five_hour": map[string]any{"utilization": account.fiveHour},
			"seven_day": map[string]any{"utilization": account.sevenDay},
		})
		return respond(http.StatusOK, string(body)), nil
	case "/api/oauth/profile":
		return respond(http.StatusOK, fmt.Sprintf(`{"account":{"uuid":%q}}`, account.uuid)), nil
	}

	return respond(http.StatusNotFound, `{}`), nil
}

func (api *fakeAnthropic) answerRefresh(request *http.Request) (*http.Response, error) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return nil, err
	}

	refreshed, known := api.refreshes[body.RefreshToken]
	if !known {
		return respond(http.StatusBadRequest, `{"error":"invalid_grant","detail":"secret-detail"}`), nil
	}

	encoded, _ := json.Marshal(map[string]any{
		"access_token":  refreshed.AccessToken,
		"refresh_token": refreshed.RefreshToken,
		"expires_in":    refreshed.ExpiresIn,
	})
	return respond(http.StatusOK, string(encoded)), nil
}

func respond(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: http.Header{}}
}

// world is one isolated machine: fake Keychain, home directory and API.
type world struct {
	t           *testing.T
	home        string
	keychainDir string
	api         *fakeAnthropic
}

func newWorld(t *testing.T) *world {
	t.Helper()

	home := t.TempDir()
	keychainDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(script, []byte(fakeSecurityScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("FAKE_KEYCHAIN_DIR", keychainDir)

	api := &fakeAnthropic{byToken: map[string]apiAccount{}, refreshes: map[string]tokenResponse{}}
	previousBin, previousClient, previousWait := securityBin, httpClient, claudeLockWait
	securityBin = script
	httpClient = &http.Client{Transport: api}
	claudeLockWait = 50 * time.Millisecond
	t.Cleanup(func() {
		securityBin, httpClient, claudeLockWait = previousBin, previousClient, previousWait
	})

	w := &world{t: t, home: home, keychainDir: keychainDir, api: api}
	w.must(saveConfig(config{FiveHourThreshold: 98, SevenDayThreshold: 98}))
	return w
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func inOneHour() int64 {
	return time.Now().Add(time.Hour).UnixMilli()
}

func loginCredentials(t *testing.T, accessToken, refreshToken string, expiresAt int64) credentialItem {
	t.Helper()

	raw, err := json.Marshal(map[string]any{
		"accessToken":      accessToken,
		"refreshToken":     refreshToken,
		"expiresAt":        expiresAt,
		"scopes":           []string{"user:inference", "user:profile"},
		"subscriptionType": "max",
	})
	if err != nil {
		t.Fatal(err)
	}

	return credentialItem{claudeLoginKey: raw}
}

func identityFor(uuid string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"accountUuid":%q,"emailAddress":"person@example.test"}`, uuid))
}

// saveAccount stores an account as ccrotate add would have.
func (w *world) saveAccount(name, uuid, accessToken, refreshToken string, expiresAt int64) {
	w.t.Helper()

	w.must(saveStoredAccount(name, storedAccount{
		Credentials:  loginCredentials(w.t, accessToken, refreshToken, expiresAt),
		OAuthAccount: identityFor(uuid),
	}))

	cfg, err := loadConfig()
	w.must(err)
	cfg.Accounts = append(cfg.Accounts, savedAccount{Name: name, AccountUUID: uuid})
	w.must(saveConfig(cfg))
}

func (w *world) setUsage(accessToken, uuid string, fiveHour, sevenDay any) {
	w.api.byToken[accessToken] = apiAccount{uuid: uuid, fiveHour: fiveHour, sevenDay: sevenDay}
}

// goLive logs Claude Code in with MCP logins beside the Claude login.
func (w *world) goLive(uuid, accessToken, refreshToken string, expiresAt int64) {
	w.t.Helper()

	item := loginCredentials(w.t, accessToken, refreshToken, expiresAt)
	item[mcpOAuthKey] = json.RawMessage(`{"github":{"accessToken":"mcp-token"}}`)
	w.must(writeLiveCredentials(item))

	file := fmt.Sprintf(`{"oauthAccount":%s,"projects":{"keep":"<me>"}}`, identityFor(uuid))
	w.must(os.WriteFile(filepath.Join(w.home, ".claude.json"), []byte(file), 0o600))
}

func (w *world) liveLogin() oauthLogin {
	w.t.Helper()

	item, err := readLiveCredentials()
	w.must(err)
	login, err := item.login()
	w.must(err)
	return login
}

func (w *world) storedLogin(name string) oauthLogin {
	w.t.Helper()

	stored, err := loadStoredAccount(name)
	w.must(err)
	login, err := stored.Credentials.login()
	w.must(err)
	return login
}

func (w *world) savedNames() []string {
	w.t.Helper()

	cfg, err := loadConfig()
	w.must(err)

	names := []string{}
	for _, account := range cfg.Accounts {
		names = append(names, account.Name)
	}
	return names
}

func (w *world) cachedIdentityUUID() string {
	w.t.Helper()

	file, err := readClaudeJSON()
	w.must(err)

	var identity struct {
		AccountUUID string `json:"accountUuid"`
	}
	w.must(json.Unmarshal(file["oauthAccount"], &identity))
	return identity.AccountUUID
}
