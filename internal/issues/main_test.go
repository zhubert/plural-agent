package issues

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Disable keychain lookups for all tests in this package to ensure
	// deterministic behavior regardless of the developer's macOS Keychain state.
	// Tests that need to exercise keychain behavior can override keychainGet directly.
	keychainGet = func(string) (string, bool) { return "", false }
	os.Exit(m.Run())
}
