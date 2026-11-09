package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const agentLabel = "sh.cline.ccrotate"

// 20 usage requests an hour, under the roughly 30 per hour per account that
// claude-swap measured (src/claude_swap/poll_policy.py).
const tickIntervalSeconds = 180

const agentPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>tick</string></array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

func agentPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

func launchdDomain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func agentIsLoaded() bool {
	return exec.Command("/bin/launchctl", "print", launchdDomain()+"/"+agentLabel).Run() == nil
}

func installAgent() error {
	plistPath, err := agentPlistPath()
	if err != nil {
		return err
	}

	binary, err := os.Executable()
	if err != nil {
		return err
	}

	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}

	dir, err := stateDir()
	if err != nil {
		return err
	}

	logPath := filepath.Join(dir, "ccrotate.log")
	plist := fmt.Sprintf(agentPlistTemplate, agentLabel, xmlEscape(binary), tickIntervalSeconds, xmlEscape(logPath), xmlEscape(logPath))

	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}

	if err := writeFileAtomic(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}

	if agentIsLoaded() {
		if err := runLaunchctl("bootout", launchdDomain()+"/"+agentLabel); err != nil {
			return err
		}
	}

	return runLaunchctl("bootstrap", launchdDomain(), plistPath)
}

func uninstallAgent() error {
	plistPath, err := agentPlistPath()
	if err != nil {
		return err
	}

	if agentIsLoaded() {
		if err := runLaunchctl("bootout", launchdDomain()+"/"+agentLabel); err != nil {
			return err
		}
	}

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func runLaunchctl(args ...string) error {
	output, err := exec.Command("/bin/launchctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, strings.TrimSpace(string(output)))
	}

	return nil
}

func xmlEscape(value string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))

	return escaped.String()
}
