package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

type accountRow struct {
	name   string
	isLive bool
	usage  usageReading
	status string
}

// listAccounts reads every saved account's usage. It never refreshes the live
// account, whose refresh token Claude Code is using.
func listAccounts(out io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if len(cfg.Accounts) == 0 {
		_, err := fmt.Fprintln(out, "no saved accounts; run ccrotate add <name>")
		return err
	}

	liveUUID := liveAccountUUID()

	rows := make([]accountRow, 0, len(cfg.Accounts))
	for _, account := range cfg.Accounts {
		row := accountRow{name: account.Name, isLive: account.AccountUUID == liveUUID}

		if row.isLive {
			row.usage, row.status = readLiveUsage()
		} else {
			row.usage, row.status = readSavedUsage(account.Name)
		}

		rows = append(rows, row)
	}

	return writeAccountTable(out, rows, cfg, time.Now())
}

// liveAccountUUID reads the identity Claude Code cached for its login, which
// avoids a network call. An empty result marks no account as live.
func liveAccountUUID() string {
	file, err := readClaudeJSON()
	if err != nil {
		return ""
	}

	var identity struct {
		AccountUUID string `json:"accountUuid"`
	}
	if err := json.Unmarshal(file["oauthAccount"], &identity); err != nil {
		return ""
	}

	return identity.AccountUUID
}

func readLiveUsage() (usageReading, string) {
	live, err := readLiveCredentials()
	if err != nil {
		return usageReading{}, err.Error()
	}

	login, err := live.login()
	if err != nil {
		return usageReading{}, err.Error()
	}

	if login.expiresWithin(0) {
		return usageReading{}, "idle; Claude Code renews the token on its next message"
	}

	usage, err := fetchUsage(login.AccessToken)
	if err != nil {
		return usageReading{}, err.Error()
	}

	return usage, ""
}

func readSavedUsage(name string) (usageReading, string) {
	_, usage, err := checkSavedAccount(name)
	if err != nil {
		return usageReading{}, err.Error()
	}

	return usage, ""
}

func writeAccountTable(out io.Writer, rows []accountRow, cfg config, now time.Time) error {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "\tACCOUNT\t5H LEFT\t5H RESETS\t7D LEFT\t7D RESETS\t")

	for _, row := range rows {
		marker := ""
		if row.isLive {
			marker = "*"
		}

		if row.status != "" {
			fmt.Fprintf(table, "%s\t%s\t%s\t\t\t\t\n", marker, row.name, row.status)
			continue
		}

		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t\n", marker, row.name,
			formatLeft(row.usage.FiveHour), formatReset(row.usage.FiveHour, now),
			formatLeft(row.usage.SevenDay), formatReset(row.usage.SevenDay, now))
	}

	if err := table.Flush(); err != nil {
		return err
	}

	_, err := fmt.Fprintf(out, "\n* live account. Rotation threshold: 5h at %v%%, 7d at %v%% used.\n",
		cfg.FiveHourThreshold, cfg.SevenDayThreshold)
	return err
}

func formatLeft(window *usageWindow) string {
	used, known := window.percent()
	if !known {
		return "-"
	}

	return fmt.Sprintf("%.0f%%", max(0, 100-used))
}

func formatReset(window *usageWindow, now time.Time) string {
	if window == nil || window.ResetsAt == nil {
		return "-"
	}

	resetsAt := window.ResetsAt.Local()
	remaining := resetsAt.Sub(now).Round(time.Minute)
	if remaining <= 0 {
		return "now"
	}

	return fmt.Sprintf("in %s (%s)", formatDuration(remaining), resetsAt.Format("Mon 15:04"))
}

func formatDuration(duration time.Duration) string {
	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}

	return fmt.Sprintf("%dm", minutes)
}
