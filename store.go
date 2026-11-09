package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"syscall"
)

const (
	claudeKeychainService   = "Claude Code-credentials"
	ccrotateKeychainService = "ccrotate"
)

// mcpOAuthKey holds Claude Code's per-MCP-server logins. They belong to no
// Claude account, so they stay in the live item across a switch.
const mcpOAuthKey = "mcpOAuth"

var accountNamePattern = regexp.MustCompile(`^[A-Za-z0-9._@+-]+$`)

type savedAccount struct {
	Name        string `json:"name"`
	AccountUUID string `json:"accountUuid"`
}

// config holds no secrets; tokens live in the Keychain.
type config struct {
	Accounts          []savedAccount `json:"accounts"`
	FiveHourThreshold float64        `json:"fiveHourThreshold"`
	SevenDayThreshold float64        `json:"sevenDayThreshold"`
}

// storedAccount is what is needed to make a saved account the live login.
type storedAccount struct {
	Credentials  credentialItem  `json:"credentials"`
	OAuthAccount json.RawMessage `json:"oauthAccount"`
}

func stateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dir := filepath.Join(home, ".ccrotate")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	return dir, nil
}

// acquireStateLock serializes ccrotate commands, so an add or rm cannot
// interleave with a scheduled rotation. The kernel drops it if the process dies.
func acquireStateLock() (*os.File, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock ccrotate state: %w", err)
	}

	return file, nil
}

func configPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "config.json"), nil
}

func loadConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return config{}, err
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config{}, nil
	}
	if err != nil {
		return config{}, err
	}

	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	return cfg, nil
}

func saveConfig(cfg config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(path, append(raw, '\n'), 0o600)
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	if _, err := temp.Write(content); err != nil {
		return err
	}

	if err := temp.Chmod(mode); err != nil {
		return err
	}

	if err := temp.Sync(); err != nil {
		return err
	}

	if err := temp.Close(); err != nil {
		return err
	}

	return os.Rename(temp.Name(), path)
}

func loadStoredAccount(name string) (storedAccount, error) {
	raw, err := keychainRead(ccrotateKeychainService, name)
	if err != nil {
		return storedAccount{}, fmt.Errorf("account %s: %w", name, err)
	}

	var stored storedAccount
	if err := json.Unmarshal(raw, &stored); err != nil {
		return storedAccount{}, fmt.Errorf("account %s: stored item is not valid JSON", name)
	}

	return stored, nil
}

func saveStoredAccount(name string, stored storedAccount) error {
	raw, err := json.Marshal(stored)
	if err != nil {
		return err
	}

	return keychainWrite(ccrotateKeychainService, name, raw)
}

func liveKeychainAccount() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("find macOS login name: %w", err)
	}

	return current.Username, nil
}

func readLiveCredentials() (credentialItem, error) {
	keychainAccount, err := liveKeychainAccount()
	if err != nil {
		return nil, err
	}

	raw, err := keychainRead(claudeKeychainService, keychainAccount)
	if errors.Is(err, errKeychainItemNotFound) {
		return nil, errors.New("no Claude Code login found on this Mac")
	}
	if err != nil {
		return nil, err
	}

	var item credentialItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, errors.New("the Claude Code keychain item is not valid JSON")
	}

	return item, nil
}

func writeLiveCredentials(item credentialItem) error {
	keychainAccount, err := liveKeychainAccount()
	if err != nil {
		return err
	}

	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}

	return keychainWrite(claudeKeychainService, keychainAccount, raw)
}

func addAccount(name string) error {
	if !accountNamePattern.MatchString(name) {
		return fmt.Errorf("account name %q may only use letters, digits and . _ @ + -", name)
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	live, err := readLiveCredentials()
	if err != nil {
		return err
	}

	login, err := live.login()
	if err != nil {
		return err
	}

	accountUUID, err := fetchAccountUUID(login.AccessToken)
	if err != nil {
		return fmt.Errorf("identify the logged-in account: %w", err)
	}

	claudeJSON, err := readClaudeJSON()
	if err != nil {
		return err
	}

	oauthAccount, err := claudeJSON.oauthAccountFor(accountUUID)
	if err != nil {
		return err
	}

	alreadySaved := false
	for _, existing := range cfg.Accounts {
		sameName := existing.Name == name
		sameAccount := existing.AccountUUID == accountUUID

		if sameName && sameAccount {
			alreadySaved = true
			continue
		}
		if sameName {
			return fmt.Errorf("%s already names a different account; rm it first", name)
		}
		if sameAccount {
			return fmt.Errorf("this login is already saved as %s", existing.Name)
		}
	}

	stored := storedAccount{Credentials: live.withoutMCPLogins(), OAuthAccount: oauthAccount}
	if err := saveStoredAccount(name, stored); err != nil {
		return err
	}

	if alreadySaved {
		fmt.Printf("updated %s\n", name)
		return nil
	}

	cfg.Accounts = append(cfg.Accounts, savedAccount{Name: name, AccountUUID: accountUUID})
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("added %s\n", name)
	return nil
}

func removeAccount(name string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	remaining := make([]savedAccount, 0, len(cfg.Accounts))
	for _, existing := range cfg.Accounts {
		if existing.Name != name {
			remaining = append(remaining, existing)
		}
	}

	if len(remaining) == len(cfg.Accounts) {
		return fmt.Errorf("no saved account named %s", name)
	}

	if err := keychainDelete(ccrotateKeychainService, name); err != nil && !errors.Is(err, errKeychainItemNotFound) {
		return err
	}

	cfg.Accounts = remaining
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Printf("removed %s\n", name)
	return nil
}
