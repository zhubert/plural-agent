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
	containerName := "erg-" + config.SessionID
	image := config.ContainerImage
	if image == "" {
		image = "ghcr.io/zhubert/erg"
	}

	claudeDir, err := paths.ClaudeConfigDir()
	if err != nil {
		return containerRunResult{}, fmt.Errorf("failed to determine Claude config dir: %w", err)
	}

	args := []string{
		"run", "-i", "--rm",
		"--name", containerName,
		"-v", config.WorkingDir + ":/workspace",
		"-v", claudeDir + ":/home/claude/.claude-host:ro",
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
		auth.Source = "$CLAUDE_CONFIG_DIR/.credentials.json (OAuth via claude login)"
	}

	// Mount MCP config for AskUserQuestion/ExitPlanMode support.
	// The MCP subprocess inside the container listens on a port and the host
	// dials in (reverse TCP direction to avoid macOS firewall issues).
	if config.MCPConfigPath != "" {
		args = append(args, "-v", config.MCPConfigPath+":"+containerMCPConfigPath+":ro")
	}

	// Forward the host's git identity into the container via env vars.
	// We use three mechanisms to prevent Claude Code from inventing its own
	// identity (e.g., "erg agent"):
	//   1. GIT_AUTHOR_*/GIT_COMMITTER_* — used by git-commit directly
	//   2. GIT_CONFIG_COUNT/KEY/VALUE — makes `git config --get user.name`
	//      return the value, so Claude Code sees identity is already configured
	//      and won't run `git config user.name` (which writes to .git/config)
	name := gitConfigValue("user.name")
	email := gitConfigValue("user.email")
	if name != "" {
		args = append(args, "-e", "GIT_AUTHOR_NAME="+name, "-e", "GIT_COMMITTER_NAME="+name)
	}
	if email != "" {
		args = append(args, "-e", "GIT_AUTHOR_EMAIL="+email, "-e", "GIT_COMMITTER_EMAIL="+email)
	}
	if configEnvs := gitConfigEnvVars(name, email); len(configEnvs) > 0 {
		for _, e := range configEnvs {
			args = append(args, "-e", e)
		}
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

// ContainerAuthSource returns a human-readable description of where container
// credentials will come from. Returns empty string if no credentials are found.
func ContainerAuthSource() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "ANTHROPIC_API_KEY env var"
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		return "CLAUDE_CODE_OAUTH_TOKEN env var"
	}
	if cred := readKeychainCredential(); cred.Value != "" {
		return cred.Source
	}
	if credentialsFileExists() {
		return "$CLAUDE_CONFIG_DIR/.credentials.json (OAuth via claude login)"
	}
	return ""
}

// ContainerAuthAvailable checks whether credentials are available for
// container mode. Returns true if any of the following are set:
//   - ANTHROPIC_API_KEY environment variable
//   - CLAUDE_CODE_OAUTH_TOKEN environment variable (long-lived token from "claude setup-token")
//   - "anthropic_api_key", "Claude Code", or "Claude Code-credentials" macOS keychain entry
//   - $CLAUDE_CONFIG_DIR/.credentials.json file (from "claude login" interactive OAuth; defaults to ~/.claude)
func ContainerAuthAvailable() bool {
	return ContainerAuthSource() != ""
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

// oauthNeedsRefresh checks parsed keychain OAuth credentials and returns true
// when the access token is missing or expired. This is the shared decision
// logic used by both readKeychainOAuthToken and KeychainNeedsRefresh.
func oauthNeedsRefresh(creds keychainOAuthCredentials) bool {
	return creds.ClaudeAiOauth.AccessToken == "" ||
		(creds.ClaudeAiOauth.ExpiresAt > 0 && time.Now().UnixMilli() >= creds.ClaudeAiOauth.ExpiresAt)
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

	if oauthNeedsRefresh(creds) {
		return ""
	}

	return creds.ClaudeAiOauth.AccessToken
}

// KeychainNeedsRefresh reports whether the macOS keychain has an OAuth entry
// whose access token is missing or expired. When true, running `claude -v` on
// the host will force the CLI to refresh the token before we read it.
func KeychainNeedsRefresh() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	raw := readKeychainPassword("Claude Code-credentials")
	if raw == "" {
		return false
	}
	var creds keychainOAuthCredentials
	if err := json.Unmarshal([]byte(raw), &creds); err != nil {
		return false
	}
	return oauthNeedsRefresh(creds)
}

// credentialsFileExists checks whether .credentials.json exists in the Claude config directory.
// This file is created by "claude login" (interactive OAuth) and contains
// refresh tokens that Claude CLI can use to obtain access tokens.
func credentialsFileExists() bool {
	claudeDir, err := paths.ClaudeConfigDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(claudeDir, ".credentials.json"))
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

// gitConfigValue reads a git config value from the host machine.
// Returns empty string if the key is not set or git is not available.
func gitConfigValue(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// appendGitIdentityEnv appends git identity env vars to the given environment
// slice if the host has git user.name/user.email configured and they aren't
// already set. Uses both GIT_AUTHOR_*/GIT_COMMITTER_* (for commits) and
// GIT_CONFIG_COUNT/KEY/VALUE (so `git config --get` returns the values,
// preventing Claude Code from writing to .git/config).
func appendGitIdentityEnv(env []string) []string {
	has := func(prefix string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, prefix+"=") {
				return true
			}
		}
		return false
	}

	name := gitConfigValue("user.name")
	email := gitConfigValue("user.email")

	if name != "" {
		if !has("GIT_AUTHOR_NAME") {
			env = append(env, "GIT_AUTHOR_NAME="+name)
		}
		if !has("GIT_COMMITTER_NAME") {
			env = append(env, "GIT_COMMITTER_NAME="+name)
		}
	}
	if email != "" {
		if !has("GIT_AUTHOR_EMAIL") {
			env = append(env, "GIT_AUTHOR_EMAIL="+email)
		}
		if !has("GIT_COMMITTER_EMAIL") {
			env = append(env, "GIT_COMMITTER_EMAIL="+email)
		}
	}

	// Also inject via GIT_CONFIG_COUNT so `git config --get user.name`
	// returns the value. Without this, Claude Code thinks git identity
	// is unconfigured and writes to .git/config.
	if !has("GIT_CONFIG_COUNT") {
		env = append(env, gitConfigEnvVars(name, email)...)
	}

	return env
}

// gitConfigEnvVars returns GIT_CONFIG_COUNT/KEY/VALUE env var entries that
// inject user.name and user.email into git's runtime config. This makes
// `git config --get user.name` return the value without any file being written.
func gitConfigEnvVars(name, email string) []string {
	var pairs []struct{ key, value string }
	if name != "" {
		pairs = append(pairs, struct{ key, value string }{"user.name", name})
	}
	if email != "" {
		pairs = append(pairs, struct{ key, value string }{"user.email", email})
	}
	if len(pairs) == 0 {
		return nil
	}

	envs := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(pairs))}
	for i, p := range pairs {
		envs = append(envs,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, p.key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, p.value),
		)
	}
	return envs
}

// FindAuthFiles returns the paths of all erg-auth-* files in the config directory.
func FindAuthFiles() ([]string, error) {
	dir := containerAuthDir()
	if dir == "" {
		return nil, nil
	}
	return filepath.Glob(filepath.Join(dir, "erg-auth-*"))
}

// ClearAuthFiles removes all erg-auth-* files from the config directory.
// Returns the number of files removed.
func ClearAuthFiles() (int, error) {
	files, err := FindAuthFiles()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, f := range files {
		if err := os.Remove(f); err == nil {
			count++
		} else if !os.IsNotExist(err) {
			return count, err
		}
	}
	return count, nil
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
