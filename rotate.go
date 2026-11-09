package main

import (
	"errors"
	"fmt"
	"log"
	"time"
)

// refreshMargin is how close to expiry a saved account's access token may get
// before it is refreshed, so it stays valid through the checks and the switch.
const refreshMargin = 5 * time.Minute

// tick is one scheduled check, run by launchd. A failed check is only logged,
// because the next tick is the retry.
func tick() error {
	if err := rotateIfNeeded(); err != nil {
		log.Printf("error: %v", err)
	}

	return nil
}

func rotateIfNeeded() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if cfg.FiveHourThreshold <= 0 || cfg.SevenDayThreshold <= 0 {
		return errors.New("thresholds are not set; run ccrotate on")
	}

	live, err := readLiveCredentials()
	if err != nil {
		return err
	}

	liveLogin, err := live.login()
	if err != nil {
		return err
	}

	// Claude Code owns the live token and refreshes it on the next message.
	// Refreshing it here would spend a single-use token that sessions still hold.
	if liveLogin.expiresWithin(0) {
		log.Print("live token has expired, Claude Code looks idle; holding")
		return nil
	}

	usage, err := fetchUsage(liveLogin.AccessToken)
	if err != nil {
		return err
	}

	if !usage.reachesThresholds(cfg) {
		log.Printf("ok: %s", usage)
		return nil
	}

	accountUUID, err := fetchAccountUUID(liveLogin.AccessToken)
	if err != nil {
		return err
	}

	activeIndex := indexOfAccount(cfg.Accounts, accountUUID)
	if activeIndex < 0 {
		log.Printf("live login is at %s but is not a saved account; not rotating", usage)
		return nil
	}

	active := cfg.Accounts[activeIndex]
	log.Printf("%s reached a threshold: %s", active.Name, usage)

	nextName, next, found := findNextAccount(cfg, activeIndex)
	if !found {
		log.Print("no other account has room; staying")
		return nil
	}

	if err := switchLogin(active.Name, next, liveLogin.AccessToken); err != nil {
		return fmt.Errorf("switch %s to %s: %w", active.Name, nextName, err)
	}

	log.Printf("rotated %s to %s", active.Name, nextName)
	return nil
}

func indexOfAccount(accounts []savedAccount, accountUUID string) int {
	for index, account := range accounts {
		if account.AccountUUID == accountUUID {
			return index
		}
	}

	return -1
}

// findNextAccount walks the accounts after the active one, wrapping around, and
// skips any it cannot verify has room.
func findNextAccount(cfg config, activeIndex int) (string, storedAccount, bool) {
	accountCount := len(cfg.Accounts)
	for offset := 1; offset < accountCount; offset++ {
		candidate := cfg.Accounts[(activeIndex+offset)%accountCount]

		stored, usage, err := checkSavedAccount(candidate.Name)
		if err != nil {
			log.Printf("skipping %s: %v", candidate.Name, err)
			continue
		}

		if usage.reachesThresholds(cfg) {
			log.Printf("skipping %s: %s", candidate.Name, usage)
			continue
		}

		return candidate.Name, stored, true
	}

	return "", storedAccount{}, false
}

func checkSavedAccount(name string) (storedAccount, usageReading, error) {
	stored, err := loadStoredAccount(name)
	if err != nil {
		return storedAccount{}, usageReading{}, err
	}

	login, err := stored.Credentials.login()
	if err != nil {
		return storedAccount{}, usageReading{}, err
	}

	if login.expiresWithin(refreshMargin) {
		refreshed, err := refreshTokens(login)
		if err != nil {
			return storedAccount{}, usageReading{}, err
		}

		if err := stored.Credentials.applyRefresh(refreshed); err != nil {
			return storedAccount{}, usageReading{}, err
		}

		if err := saveStoredAccount(name, stored); err != nil {
			return storedAccount{}, usageReading{}, err
		}

		login, err = stored.Credentials.login()
		if err != nil {
			return storedAccount{}, usageReading{}, err
		}
	}

	usage, err := fetchUsage(login.AccessToken)
	if err != nil {
		return storedAccount{}, usageReading{}, err
	}

	return stored, usage, nil
}

// switchLogin saves the outgoing account's newest tokens, then installs the
// next account. It runs under Claude Code's locks and makes no network calls,
// so those locks are held only briefly.
func switchLogin(activeName string, next storedAccount, checkedAccessToken string) error {
	locks, err := acquireClaudeLocks()
	if err != nil {
		return err
	}
	defer locks.release()

	live, err := readLiveCredentials()
	if err != nil {
		return err
	}

	liveLogin, err := live.login()
	if err != nil {
		return err
	}

	// A different token means Claude Code refreshed, or someone ran /login,
	// after the usage check. The saved decision may no longer describe this login.
	if liveLogin.AccessToken != checkedAccessToken {
		return errors.New("the live login changed during the check; retrying next tick")
	}

	// Parsed before the Keychain changes, so a malformed file aborts cleanly.
	claudeConfig, err := readClaudeJSON()
	if err != nil {
		return err
	}

	outgoing, err := loadStoredAccount(activeName)
	if err != nil {
		return err
	}

	// Saved first: the refresh token in the live item is the only valid one.
	outgoing.Credentials = live.withoutMCPLogins()
	if err := saveStoredAccount(activeName, outgoing); err != nil {
		return err
	}

	incoming := next.Credentials.withoutMCPLogins()
	if mcpLogins, hasMCPLogins := live[mcpOAuthKey]; hasMCPLogins {
		incoming[mcpOAuthKey] = mcpLogins
	}

	if err := writeLiveCredentials(incoming); err != nil {
		if restoreErr := writeLiveCredentials(live); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore previous login: %w", restoreErr))
		}

		return err
	}

	claudeConfig["oauthAccount"] = next.OAuthAccount
	if err := writeClaudeJSON(claudeConfig); err != nil {
		return fmt.Errorf("login switched, but updating ~/.claude.json failed: %w", err)
	}

	return nil
}
