package claude

import (
	"strings"
	"testing"
)

func TestRedactor_Redact(t *testing.T) {
	tests := []struct {
		name    string
		secrets []string
		input   string
		want    string
	}{
		{
			name:    "no secrets configured",
			secrets: nil,
			input:   "hello world",
			want:    "hello world",
		},
		{
			name:    "single secret replaced",
			secrets: []string{"sk-ant-abc123"},
			input:   `{"api_key":"sk-ant-abc123"}`,
			want:    `{"api_key":"[REDACTED]"}`,
		},
		{
			name:    "multiple secrets replaced",
			secrets: []string{"token-abc", "key-xyz"},
			input:   "use token-abc and key-xyz",
			want:    "use [REDACTED] and [REDACTED]",
		},
		{
			name:    "secret appears multiple times",
			secrets: []string{"s3cr3t"},
			input:   "s3cr3t is s3cr3t",
			want:    "[REDACTED] is [REDACTED]",
		},
		{
			name:    "no secret present in text",
			secrets: []string{"sk-ant-abc123"},
			input:   "no sensitive data here",
			want:    "no sensitive data here",
		},
		{
			name:    "empty input",
			secrets: []string{"sk-ant-abc123"},
			input:   "",
			want:    "",
		},
		{
			name:    "secret in JSON stream line",
			secrets: []string{"sk-ant-realkey"},
			input:   `{"type":"result","content":"key is sk-ant-realkey done"}`,
			want:    `{"type":"result","content":"key is [REDACTED] done"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &Redactor{secrets: tc.secrets}
			got := r.Redact(tc.input)
			if got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestNewRedactor_ReadsEnvVars(t *testing.T) {
	// Set known env var and confirm NewRedactor picks it up
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
	t.Setenv("GITHUB_TOKEN", "ghp_testtoken")

	r := NewRedactor()

	if len(r.secrets) < 2 {
		t.Fatalf("expected at least 2 secrets, got %d", len(r.secrets))
	}

	input := "key=sk-ant-test-key token=ghp_testtoken"
	got := r.Redact(input)

	if strings.Contains(got, "sk-ant-test-key") {
		t.Error("ANTHROPIC_API_KEY value not redacted")
	}
	if strings.Contains(got, "ghp_testtoken") {
		t.Error("GITHUB_TOKEN value not redacted")
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Error("expected [REDACTED] placeholder in output")
	}
}

func TestNewRedactor_IgnoresEmptyEnvVars(t *testing.T) {
	// Unset all known vars to ensure empty values are skipped
	for _, name := range knownSecretEnvVars {
		t.Setenv(name, "")
	}

	r := NewRedactor()

	if len(r.secrets) != 0 {
		t.Errorf("expected 0 secrets when env vars are empty, got %d", len(r.secrets))
	}
}

func TestNewRedactor_AllKnownVarsRecognised(t *testing.T) {
	// Each known env var should be collected when set
	for _, name := range knownSecretEnvVars {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "super-secret-value")
			r := NewRedactor()
			found := false
			for _, s := range r.secrets {
				if s == "super-secret-value" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("env var %s not collected by NewRedactor", name)
			}
		})
	}
}
