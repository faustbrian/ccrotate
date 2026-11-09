package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const claudeLoginKey = "claudeAiOauth"

// credentialItem is the JSON object Claude Code keeps in its Keychain item.
// Keys are left as raw JSON so fields ccrotate does not know about survive.
type credentialItem map[string]json.RawMessage

type oauthLogin struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    int64    `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

func (item credentialItem) login() (oauthLogin, error) {
	raw, found := item[claudeLoginKey]
	if !found {
		return oauthLogin{}, errors.New("credentials hold no Claude login")
	}

	var login oauthLogin
	if err := json.Unmarshal(raw, &login); err != nil {
		return oauthLogin{}, errors.New("the Claude login in the credentials is malformed")
	}

	if login.AccessToken == "" || login.RefreshToken == "" {
		return oauthLogin{}, errors.New("the Claude login is missing its tokens")
	}

	return login, nil
}

func (login oauthLogin) expiresWithin(margin time.Duration) bool {
	return time.Now().Add(margin).UnixMilli() >= login.ExpiresAt
}

func (item credentialItem) withoutMCPLogins() credentialItem {
	copied := make(credentialItem, len(item))
	for key, value := range item {
		if key != mcpOAuthKey {
			copied[key] = value
		}
	}

	return copied
}

// applyRefresh replaces the tokens and keeps the login's other fields, such as
// its subscription type.
func (item credentialItem) applyRefresh(refreshed tokenResponse) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item[claudeLoginKey], &fields); err != nil {
		return fmt.Errorf("the Claude login in the credentials is malformed")
	}

	updates := map[string]any{
		"accessToken":  refreshed.AccessToken,
		"refreshToken": refreshed.RefreshToken,
		"expiresAt":    time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second).UnixMilli(),
	}
	if refreshed.Scope != "" {
		updates["scopes"] = strings.Fields(refreshed.Scope)
	}

	for key, value := range updates {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		fields[key] = encoded
	}

	login, err := json.Marshal(fields)
	if err != nil {
		return err
	}

	item[claudeLoginKey] = login
	return nil
}
