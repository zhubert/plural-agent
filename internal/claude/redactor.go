package claude

import (
	"os"
	"strings"
)

// knownSecretEnvVars is the list of environment variable names whose values
// should never appear in transcripts or stream logs.
var knownSecretEnvVars = []string{
	"ANTHROPIC_API_KEY",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"LINEAR_API_KEY",
	"ASANA_PAT",
	"GITHUB_TOKEN",
}

// Redactor replaces known secret values with a placeholder to prevent
// sensitive data from appearing in transcripts and stream log files.
type Redactor struct {
	secrets []string
}

// NewRedactor creates a Redactor populated with secret values read from the
// current environment. Non-empty values of knownSecretEnvVars are collected
// so they can be scrubbed from any text that passes through Redact.
func NewRedactor() *Redactor {
	var secrets []string
	for _, name := range knownSecretEnvVars {
		if val := os.Getenv(name); val != "" {
			secrets = append(secrets, val)
		}
	}
	return &Redactor{secrets: secrets}
}

// Redact replaces every occurrence of a known secret value in text with
// "[REDACTED]". Returns text unchanged when no secrets are configured.
func (r *Redactor) Redact(text string) string {
	for _, secret := range r.secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return text
}
