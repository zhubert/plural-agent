package secrets

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

const keychainAccount = "erg"

// Keychain service names for issue tracker tokens.
const (
	AsanaPATService     = "erg/ASANA_PAT"
	LinearAPIKeyService = "erg/LINEAR_API_KEY"
)

// TokenNotFoundError returns an error message appropriate for the current platform.
// On macOS it suggests using 'erg configure' to store in the Keychain;
// on other platforms it only mentions the environment variable.
func TokenNotFoundError(envVar string) string {
	if IsKeychainAvailable() {
		return envVar + " not found (set env var or run 'erg configure' to store in macOS Keychain)"
	}
	return envVar + " not found (set the " + envVar + " environment variable)"
}

// Get retrieves a secret from the macOS Keychain by service name.
// Returns the value and true if found, or ("", false) on non-macOS platforms,
// if the entry doesn't exist, or on error.
func Get(service string) (string, bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", keychainAccount, "-w").Output()
	if err != nil {
		return "", false
	}
	val := strings.TrimSpace(string(out))
	if val == "" {
		return "", false
	}
	return val, true
}

// Set stores a secret in the macOS Keychain. Uses -U to update if it already exists.
// Returns an error on non-macOS platforms.
func Set(service, value string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("keychain storage is only available on macOS")
	}
	return exec.Command("security", "add-generic-password", "-s", service, "-a", keychainAccount, "-w", value, "-U").Run()
}

// Delete removes a secret from the macOS Keychain.
// Returns nil on non-macOS platforms (no-op).
func Delete(service string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return exec.Command("security", "delete-generic-password", "-s", service, "-a", keychainAccount).Run()
}

// IsKeychainAvailable returns true if the current platform supports keychain storage.
func IsKeychainAvailable() bool {
	return runtime.GOOS == "darwin"
}
