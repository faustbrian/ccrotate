package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// These are the private endpoints and OAuth client Claude Code itself uses, as
// read from clauth (src/oauth.rs) and claude-swap (src/claude_swap/oauth.py).
// A Claude Code release can change them.
const (
	usageURL       = "https://api.anthropic.com/api/oauth/usage"
	profileURL     = "https://api.anthropic.com/api/oauth/profile"
	tokenURL       = "https://platform.claude.com/v1/oauth/token"
	oauthClientID  = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	oauthBeta      = "oauth-2025-04-20"
	userAgent      = "ccrotate/0.1"
	requestTimeout = 15 * time.Second
)

var httpClient = &http.Client{Timeout: requestTimeout}

// usageWindow is a nil-able percentage: the usage API allows utilization to be
// null, and omits windows an account's plan does not have.
type usageWindow struct {
	Utilization *float64 `json:"utilization"`
}

type usageReading struct {
	FiveHour *usageWindow `json:"five_hour"`
	SevenDay *usageWindow `json:"seven_day"`
}

func (window *usageWindow) percent() (float64, bool) {
	if window == nil || window.Utilization == nil {
		return 0, false
	}

	return *window.Utilization, true
}

func (reading usageReading) reachesThresholds(cfg config) bool {
	fiveHour, hasFiveHour := reading.FiveHour.percent()
	sevenDay, hasSevenDay := reading.SevenDay.percent()

	fiveHourReached := hasFiveHour && fiveHour >= cfg.FiveHourThreshold
	sevenDayReached := hasSevenDay && sevenDay >= cfg.SevenDayThreshold

	return fiveHourReached || sevenDayReached
}

func (reading usageReading) String() string {
	return fmt.Sprintf("5h=%s 7d=%s", formatPercent(reading.FiveHour), formatPercent(reading.SevenDay))
}

func formatPercent(window *usageWindow) string {
	percent, known := window.percent()
	if !known {
		return "n/a"
	}

	return fmt.Sprintf("%.0f%%", percent)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

func fetchUsage(accessToken string) (usageReading, error) {
	var reading usageReading
	if err := getJSON(usageURL, accessToken, &reading); err != nil {
		return usageReading{}, fmt.Errorf("usage: %w", err)
	}

	_, hasFiveHour := reading.FiveHour.percent()
	_, hasSevenDay := reading.SevenDay.percent()
	if !hasFiveHour && !hasSevenDay {
		return usageReading{}, errors.New("usage: response has neither a 5h nor a 7d reading")
	}

	return reading, nil
}

func fetchAccountUUID(accessToken string) (string, error) {
	var profile struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
	}
	if err := getJSON(profileURL, accessToken, &profile); err != nil {
		return "", fmt.Errorf("profile: %w", err)
	}

	if profile.Account.UUID == "" {
		return "", errors.New("profile: response has no account uuid")
	}

	return profile.Account.UUID, nil
}

func getJSON(url, accessToken string, target any) error {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("anthropic-beta", oauthBeta)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)

	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return errors.New("response is not the expected JSON")
	}

	return nil
}

// refreshTokens spends the login's single-use refresh token, so the caller
// must persist the result before doing anything else.
func refreshTokens(login oauthLogin) (tokenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": login.RefreshToken,
		"client_id":     oauthClientID,
		"scope":         strings.Join(login.Scopes, " "),
	})
	if err != nil {
		return tokenResponse{}, err
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return tokenResponse{}, err
	}

	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)

	response, err := httpClient.Do(request)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token refresh: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("token refresh: %s", describeOAuthFailure(response))
	}

	var refreshed tokenResponse
	if err := json.NewDecoder(response.Body).Decode(&refreshed); err != nil {
		return tokenResponse{}, errors.New("token refresh: response is not the expected JSON")
	}

	if refreshed.AccessToken == "" || refreshed.RefreshToken == "" || refreshed.ExpiresIn <= 0 {
		return tokenResponse{}, errors.New("token refresh: response is missing tokens")
	}

	return refreshed, nil
}

// describeOAuthFailure reports only the status and the OAuth error code, so
// nothing else from the response body can reach the log.
func describeOAuthFailure(response *http.Response) string {
	var failure struct {
		Error string `json:"error"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err == nil && json.Unmarshal(body, &failure) == nil && failure.Error != "" {
		return fmt.Sprintf("HTTP %d %s", response.StatusCode, failure.Error)
	}

	return fmt.Sprintf("HTTP %d", response.StatusCode)
}
