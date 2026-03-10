package issues

import (
	"fmt"
	"os"

	"github.com/zhubert/erg/internal/secrets"
)

// keychainGet is the function used to look up secrets from the keychain.
// It can be overridden in tests to control keychain behavior.
var keychainGet = secrets.Get

// resolveToken looks up an API token by checking the environment variable first,
// then falling back to the macOS Keychain. Returns the token and true if found.
func resolveToken(envVar, keychainService string) (string, bool) {
	if v := os.Getenv(envVar); v != "" {
		return v, true
	}
	return keychainGet(keychainService)
}

// tokenNotFoundErr returns a platform-appropriate error for a missing token.
func tokenNotFoundErr(envVar string) error {
	return fmt.Errorf("%s", secrets.TokenNotFoundError(envVar))
}
