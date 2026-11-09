package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func listOutput(t *testing.T) string {
	t.Helper()

	var out bytes.Buffer
	if err := listAccounts(&out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func rowFor(t *testing.T, output, name string) string {
	t.Helper()

	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, " "+name+" ") {
			return line
		}
	}
	t.Fatalf("no row for %s in:\n%s", name, output)
	return ""
}

func TestListShowsUsageLeftAndMarksTheLiveAccount(t *testing.T) {
	w := threeAccounts(t)
	w.setUsage("b-access", "uuid-b", 25.0, 60.0)

	output := listOutput(t)

	live := rowFor(t, output, "a")
	if !strings.HasPrefix(live, "*") || !strings.Contains(live, "100%") || !strings.Contains(live, "2%") {
		t.Fatalf("live row = %q, want * with 100%% and 2%% left", live)
	}

	saved := rowFor(t, output, "b")
	if strings.HasPrefix(saved, "*") || !strings.Contains(saved, "75%") || !strings.Contains(saved, "40%") {
		t.Fatalf("saved row = %q, want 75%% and 40%% left", saved)
	}
}

func TestListReadsTheLiveTokenNotTheSavedCopy(t *testing.T) {
	w := threeAccounts(t)

	listOutput(t)

	for _, path := range w.api.requests {
		if path == "/v1/oauth/token" {
			t.Fatal("ls refreshed a token")
		}
	}
	if got := w.storedLogin("a").AccessToken; got != "a-stored-access" {
		t.Fatalf("ls changed the live account's saved copy: %q", got)
	}
}

func TestListRefreshesAnExpiredSavedAccountAndKeepsTheNewTokens(t *testing.T) {
	w := threeAccounts(t)
	w.saveAccount("old", "uuid-old", "old-access", "old-refresh", time.Now().Add(-time.Hour).UnixMilli())
	w.api.refreshes["old-refresh"] = tokenResponse{AccessToken: "new-access", RefreshToken: "new-refresh", ExpiresIn: 28800}
	w.setUsage("new-access", "uuid-old", 50.0, 50.0)

	output := listOutput(t)

	if !strings.Contains(rowFor(t, output, "old"), "50%") {
		t.Fatalf("refreshed account row missing usage:\n%s", output)
	}
	if got := w.storedLogin("old").RefreshToken; got != "new-refresh" {
		t.Fatalf("spent refresh token kept: %q", got)
	}
}

func TestListReportsAnUnreadableAccountAndContinues(t *testing.T) {
	w := threeAccounts(t)
	delete(w.api.byToken, "b-access")

	output := listOutput(t)

	if !strings.Contains(rowFor(t, output, "b"), "HTTP 401") {
		t.Fatalf("unreadable row lacks its reason:\n%s", output)
	}
	if !strings.Contains(rowFor(t, output, "c"), "80%") {
		t.Fatalf("listing stopped after the failing account:\n%s", output)
	}
}

func TestListLeavesAnIdleLiveAccountAlone(t *testing.T) {
	w := threeAccounts(t)
	w.goLive("uuid-a", "a-live-access", "a-live-refresh", time.Now().Add(-time.Minute).UnixMilli())

	output := listOutput(t)

	if !strings.Contains(rowFor(t, output, "a"), "idle") {
		t.Fatalf("expired live token not reported as idle:\n%s", output)
	}
}

func TestListWithNoAccounts(t *testing.T) {
	newWorld(t)

	if output := listOutput(t); !strings.Contains(output, "no saved accounts") {
		t.Fatalf("output = %q", output)
	}
}

func TestFormatReset(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.Local)
	at := func(offset time.Duration) *usageWindow {
		resetsAt := now.Add(offset)
		return &usageWindow{ResetsAt: &resetsAt}
	}

	cases := []struct {
		name   string
		window *usageWindow
		want   string
	}{
		{"minutes", at(42 * time.Minute), "in 42m (Mon 10:42)"},
		{"hours", at(3*time.Hour + 20*time.Minute), "in 3h 20m (Mon 13:20)"},
		{"days", at(6*24*time.Hour + 8*time.Hour), "in 6d 8h (Sun 18:00)"},
		{"already passed", at(-time.Minute), "now"},
		{"unknown", &usageWindow{}, "-"},
		{"window absent", nil, "-"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := formatReset(testCase.window, now); got != testCase.want {
				t.Fatalf("formatReset = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestUsageResetTimeParsesTheAPIFormat(t *testing.T) {
	w := newWorld(t)
	w.api.byToken["token"] = apiAccount{uuid: "uuid", fiveHour: 1.0, sevenDay: 1.0, resetsAt: "2026-11-09T13:19:59.785938+00:00"}

	usage, err := fetchUsage("token")
	w.must(err)

	want := time.Date(2026, 11, 9, 13, 19, 59, 785938000, time.UTC)
	if usage.FiveHour.ResetsAt == nil || !usage.FiveHour.ResetsAt.Equal(want) {
		t.Fatalf("resets_at = %v, want %v", usage.FiveHour.ResetsAt, want)
	}
}

func TestFormatLeft(t *testing.T) {
	if got := formatLeft(window(98)); got != "2%" {
		t.Fatalf("98%% used = %q", got)
	}
	if got := formatLeft(window(104)); got != "0%" {
		t.Fatalf("over 100%% used = %q", got)
	}
	if got := formatLeft(nil); got != "-" {
		t.Fatalf("absent window = %q", got)
	}
}
