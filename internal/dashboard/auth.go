package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	iexec "github.com/zhubert/erg/internal/exec"
)

// AuthInfo holds the parsed output of `claude auth status`.
type AuthInfo struct {
	Email        string    `json:"email,omitempty"`
	AccountUUID  string    `json:"-"`
	Subscription string    `json:"subscription,omitempty"`
	IsLoggedIn   bool      `json:"is_logged_in"`
	FetchedAt    time.Time `json:"fetched_at"`
}

// rawAuthStatus mirrors the JSON structure of `claude auth status` output.
type rawAuthStatus struct {
	ClaudeAiOAuthAccount *rawOAuthAccount `json:"claudeAiOAuthAccount"`
	IsLoggedIn           bool             `json:"isLoggedIn"`
}

type rawOAuthAccount struct {
	EmailAddress string `json:"emailAddress"`
	UUID         string `json:"uuid"`
	Subscription string `json:"subscription"`
}

// FetchAuthInfo runs `claude auth status` and parses its JSON output into an
// AuthInfo. Unknown JSON fields are silently ignored so the function stays
// compatible with future CLI versions.
//
// Returns an error if the subprocess fails or the output is not valid JSON.
func FetchAuthInfo(ctx context.Context, ex iexec.CommandExecutor) (*AuthInfo, error) {
	out, err := ex.Output(ctx, "", "claude", "auth", "status")
	if err != nil {
		return nil, fmt.Errorf("claude auth status: %w", err)
	}

	var raw rawAuthStatus
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse auth status: %w", err)
	}

	info := &AuthInfo{
		IsLoggedIn: raw.IsLoggedIn,
		FetchedAt:  time.Now(),
	}
	if raw.ClaudeAiOAuthAccount != nil {
		info.Email = raw.ClaudeAiOAuthAccount.EmailAddress
		info.AccountUUID = raw.ClaudeAiOAuthAccount.UUID
		info.Subscription = raw.ClaudeAiOAuthAccount.Subscription
	}

	return info, nil
}
