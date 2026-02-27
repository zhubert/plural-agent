package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zhubert/erg/internal/paths"
)

// containerRunResult holds the result of building container run arguments.
type containerRunResult struct {
	Args       []string // Arguments for `docker run`
	AuthSource string   // Credential source used (empty if none)
}

// buildContainerRunArgs constructs the arguments for `docker run` that wraps
// the Claude CLI process inside a Docker container.
func buildContainerRunArgs(config ProcessConfig, claudeArgs []string) (containerRunResult, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return containerRunResult{}, fmt.Errorf("failed to determine home directory: %w", err)
	}

	containerName := "erg-" + config.SessionID
	image := config.ContainerImage
	if image == "" {
		image = "ghcr.io/zhubert/erg"
	}

	args := []string{
		"run", "-i", "--rm",
		"--name", containerName,
		"-v", config.WorkingDir + ":/workspace",
		"-v", homeDir + "/.claude:/home/claude/.claude-host:ro",
		"-w", "/workspace",
	}

	// Publish the container MCP port so the host can dial in.
	// -p 0:<port> maps an ephemeral host port to the fixed container port.
	// The host discovers the mapped port via `docker port`.
	if config.ContainerMCPPort > 0 {
		args = append(args, "-p", fmt.Sprintf("0:%d", config.ContainerMCPPort))
	}

	// Pass ERG_SKIP_UPDATE through to the container if set on the host.
	// This allows developers to skip the entrypoint auto-update when testing
	// with a locally-built container image.
	if os.Getenv("ERG_SKIP_UPDATE") != "" {
		args = append(args, "-e", "ERG_SKIP_UPDATE=1")
	}

	// Pass auth credentials via --env-file.
	// On macOS, Claude Code stores auth in the system keychain which isn't
	// accessible inside a Linux container. We write the key to a temp file
	// (0600 permissions) and pass it via --env-file, which sets the env var
	// directly in the container process. This is safer than -e which would
	// expose the key in `ps` output on the host.
	auth := writeContainerAuthFile(config.SessionID)
	if auth.Path != "" {
		args = append(args, "--env-file", auth.Path)
	} else if credentialsFileExists() {
		// No env var or keychain credentials, but .credentials.json exists on the host.
		// The entrypoint copies it into the container's ~/.claude/, so Claude CLI
		// will find it and handle token refresh natively. No --env-file needed.
		auth.Source = "~/.claude/.credentials.json (OAuth via claude login)"
	}

	// Mount MCP config for AskUserQuestion/ExitPlanMode support.
	// The MCP subprocess inside the container listens on a port and the host
	// dials in (reverse TCP direction to avoid macOS firewall issues).
	if config.MCPConfigPath != "" {
		args = append(args, "-v", config.MCPConfigPath+":"+containerMCPConfigPath+":ro")
	}

	// Mount main repository for git worktree support.
	// Git worktrees have a .git file pointing to /path/to/repo/.git/worktrees/<id>.
	// We mount the repo at its original absolute path so these references work transparently.
	// Note: Must be read-write because git needs to update .git/worktrees/<id>/ when committing.
	if config.RepoPath != "" {
		args = append(args, "-v", config.RepoPath+":"+config.RepoPath)
	}

	args = append(args, image)
	args = append(args, claudeArgs...)
	return containerRunResult{Args: args, AuthSource: auth.Source}, nil
}

// containerAuthDir returns the directory for storing container auth files.
// Uses the config directory which is user-private, unlike /tmp which is world-readable.
// Returns empty string if the config directory cannot be determined (credentials
// will not be written rather than falling back to an insecure location).
func containerAuthDir() string {
	dir, err := paths.ConfigDir()
	if err != nil {
		return ""
	}
	os.MkdirAll(dir, 0700)
	return dir
}

// containerAuthFilePath returns the path for a session's container auth file.
// Returns empty string if the auth directory cannot be determined.
func containerAuthFilePath(sessionID string) string {
	dir := containerAuthDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fmt.Sprintf("erg-auth-%s", sessionID))
}

// ContainerAuthAvailable checks whether credentials are available for
// container mode. Returns true if any of the following are set:
//   - ANTHROPIC_API_KEY environment variable
//   - CLAUDE_CODE_OAUTH_TOKEN environment variable (long-lived token from "claude setup-token")
//   - "anthropic_api_key", "Claude Code", or "Claude Code-credentials" macOS keychain entry
//   - ~/.claude/.credentials.json file (from "claude login" interactive OAuth)
func ContainerAuthAvailable() bool {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		return true
	}
	if cred := readKeychainCredential(); cred.Value != "" {
		return true
	}
	if credentialsFileExists() {
		return true
	}
	return false
}

// keychainCredential holds a credential read from the macOS keychain.
type keychainCredential struct {
	Value  string // The credential value (API key or OAuth access token)
	EnvVar string // The env var to set ("ANTHROPIC_API_KEY" or "CLAUDE_CODE_OAUTH_TOKEN")
	Source string // Description for logging
}

// readKeychainCredential reads credentials from the macOS keychain, checking
// (in priority order):
//  1. "anthropic_api_key" - legacy API key entry
//  2. "Claude Code" - API key for API usage billing
//  3. "Claude Code-credentials" - OAuth credentials for Pro/Max subscriptions
func readKeychainCredential() keychainCredential {
	if key := readKeychainPassword("anthropic_api_key"); key != "" {
		return keychainCredential{Value: key, EnvVar: "ANTHROPIC_API_KEY", Source: "macOS keychain (anthropic_api_key)"}
	}
	if key := readKeychainPassword("Claude Code"); key != "" {
		return keychainCredential{Value: key, EnvVar: "ANTHROPIC_API_KEY", Source: "macOS keychain (Claude Code)"}
	}
	if token := readKeychainOAuthToken(); token != "" {
		return keychainCredential{Value: token, EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Source: "macOS keychain (Claude Code-credentials)"}
	}
	return keychainCredential{}
}

// keychainOAuthCredentials represents the JSON structure stored in the
// "Claude Code-credentials" macOS keychain entry for Pro/Max subscriptions.
type keychainOAuthCredentials struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

// readKeychainOAuthToken reads an OAuth access token from the macOS keychain
// "Claude Code-credentials" entry, used by Claude Pro/Max subscriptions.
// Returns empty string if not found, expired, on error, or on non-macOS platforms.
func readKeychainOAuthToken() string {
	raw := readKeychainPassword("Claude Code-credentials")
	if raw == "" {
		return ""
	}

	var creds keychainOAuthCredentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return ""
	}

	if creds.ClaudeAiOauth.AccessToken == "" {
		return ""
	}

	// Check if token is expired
	if creds.ClaudeAiOauth.ExpiresAt > 0 && time.Now().UnixMilli() >= creds.ClaudeAiOauth.ExpiresAt {
		return ""
	}

	return creds.ClaudeAiOauth.AccessToken
}

// credentialsFileExists checks whether ~/.claude/.credentials.json exists.
// This file is created by "claude login" (interactive OAuth) and contains
// refresh tokens that Claude CLI can use to obtain access tokens.
func credentialsFileExists() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	return err == nil
}

// containerAuthResult holds the result of writing a container auth file.
type containerAuthResult struct {
	Path   string // File path, empty if no credentials available
	Source string // Credential source description for logging
}

// writeContainerAuthFile writes credentials to a file in ~/.erg/ with
// restricted permissions (0600) and returns the file path and source.
// The file is passed to Docker via --env-file, which sets the env var
// directly in the container process.
//
// File format: ENV_VAR_NAME=value (Docker env-file format, no quotes)
//
// Credential sources (in priority order):
//  1. ANTHROPIC_API_KEY from environment
//  2. CLAUDE_CODE_OAUTH_TOKEN from environment (long-lived token from "claude setup-token")
//  3. macOS keychain entry ("anthropic_api_key", "Claude Code", or "Claude Code-credentials")
//
// Note: OAuth access tokens from "Claude Code-credentials" (Pro/Max subscriptions)
// are short-lived and will expire inside the container. For long-running container
// sessions, use "claude setup-token" to generate a long-lived CLAUDE_CODE_OAUTH_TOKEN.
//
// Returns empty path if no credentials are available.
func writeContainerAuthFile(sessionID string) containerAuthResult {
	var content string
	var source string

	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		content = "ANTHROPIC_API_KEY=" + apiKey
		source = "ANTHROPIC_API_KEY env var"
	} else if oauthToken := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); oauthToken != "" {
		// Claude CLI recognizes CLAUDE_CODE_OAUTH_TOKEN directly as an environment variable
		content = "CLAUDE_CODE_OAUTH_TOKEN=" + oauthToken
		source = "CLAUDE_CODE_OAUTH_TOKEN env var"
	} else if cred := readKeychainCredential(); cred.Value != "" {
		content = cred.EnvVar + "=" + cred.Value
		source = cred.Source
	}

	if content == "" {
		return containerAuthResult{}
	}

	// Validate credential value has no newlines that would break Docker env-file format
	// (Docker env-file doesn't support multiline values)
	parts := strings.SplitN(content, "=", 2)
	if len(parts) == 2 && strings.ContainsAny(parts[1], "\n\r") {
		return containerAuthResult{}
	}

	path := containerAuthFilePath(sessionID)
	if path == "" {
		return containerAuthResult{}
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return containerAuthResult{}
	}
	return containerAuthResult{Path: path, Source: source}
}

// readKeychainPassword reads a password from the macOS keychain.
// Returns empty string if not found, on error, or on non-macOS platforms.
func readKeychainPassword(service string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
