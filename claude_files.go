package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Claude Code guards its OAuth refresh and its ~/.claude.json writes with
// proper-lockfile directories. Taking the same locks stops a refresh from
// landing in the middle of a switch and overwriting it. Order and names are
// from claude-swap (src/claude_swap/claude_locks.py, Claude Code 2.1.218).
const (
	claudeLockWait     = 9 * time.Second
	claudeLockRetry    = 200 * time.Millisecond
	claudeLockTouching = 3 * time.Second
)

// claudeLocks holds the lock directories until release. Holders are expected
// to refresh a lock's mtime, or Claude Code treats it as stale.
type claudeLocks struct {
	dirs []string
	stop chan struct{}
	done chan struct{}
}

func claudeLockDirs() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return []string{
		filepath.Join(home, ".claude", ".oauth_refresh.lock"),
		filepath.Join(home, ".claude.lock"),
		filepath.Join(home, ".claude.json.lock"),
	}, nil
}

// acquireClaudeLocks never takes over a stale lock. Claude Code recovers its
// own stale locks, and a rotation that times out simply retries on the next tick.
func acquireClaudeLocks() (*claudeLocks, error) {
	dirs, err := claudeLockDirs()
	if err != nil {
		return nil, err
	}

	locks := &claudeLocks{stop: make(chan struct{}), done: make(chan struct{})}
	for _, dir := range dirs {
		if err := waitForLockDir(dir); err != nil {
			locks.removeDirs()
			return nil, err
		}
		locks.dirs = append(locks.dirs, dir)
	}

	go locks.keepFresh()
	return locks, nil
}

func waitForLockDir(dir string) error {
	deadline := time.Now().Add(claudeLockWait)
	for {
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("lock %s: %w", dir, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lock %s is held", dir)
		}
		time.Sleep(claudeLockRetry)
	}
}

func (locks *claudeLocks) keepFresh() {
	defer close(locks.done)

	ticker := time.NewTicker(claudeLockTouching)
	defer ticker.Stop()

	for {
		select {
		case <-locks.stop:
			return
		case now := <-ticker.C:
			for _, dir := range locks.dirs {
				_ = os.Chtimes(dir, now, now)
			}
		}
	}
}

func (locks *claudeLocks) release() {
	close(locks.stop)
	<-locks.done
	locks.removeDirs()
}

func (locks *claudeLocks) removeDirs() {
	for _, dir := range locks.dirs {
		_ = os.Remove(dir)
	}
}

// claudeJSON is ~/.claude.json with every value left as raw JSON, because
// ccrotate changes only oauthAccount in a file Claude Code owns.
type claudeJSON map[string]json.RawMessage

func claudeJSONPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".claude.json"), nil
}

func readClaudeJSON() (claudeJSON, error) {
	path, err := claudeJSONPath()
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var parsed claudeJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return parsed, nil
}

// oauthAccountFor returns the cached identity block, which Claude Code trusts
// over the token on startup, after checking it belongs to the expected account.
func (file claudeJSON) oauthAccountFor(accountUUID string) (json.RawMessage, error) {
	raw, found := file["oauthAccount"]
	if !found {
		return nil, errors.New("~/.claude.json has no oauthAccount; start Claude Code once and retry")
	}

	var identity struct {
		AccountUUID string `json:"accountUuid"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		return nil, errors.New("~/.claude.json has a malformed oauthAccount")
	}

	if identity.AccountUUID != accountUUID {
		return nil, errors.New("~/.claude.json describes a different account than the current login; restart Claude Code and retry")
	}

	return raw, nil
}

func writeClaudeJSON(file claudeJSON) error {
	path, err := claudeJSONPath()
	if err != nil {
		return err
	}

	// The file may be a symlink; replacing the link would detach it from its target.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(target)
	if err != nil {
		return err
	}

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(file); err != nil {
		return err
	}

	return writeFileAtomic(target, encoded.Bytes(), info.Mode().Perm())
}
