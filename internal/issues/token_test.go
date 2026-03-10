package issues

import (
	"os"
	"testing"
)

// disableKeychainForTest replaces the keychain lookup with a no-op
// and registers a cleanup function that restores the original.
func disableKeychainForTest(t *testing.T) {
	t.Helper()
	orig := keychainGet
	keychainGet = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { keychainGet = orig })
}

func TestResolveToken_EnvVarTakesPriority(t *testing.T) {
	const envVar = "ERG_TEST_TOKEN_RESOLVE"
	os.Setenv(envVar, "from-env")
	defer os.Unsetenv(envVar)

	// Even if keychain would return something, env var wins
	orig := keychainGet
	keychainGet = func(string) (string, bool) { return "from-keychain", true }
	defer func() { keychainGet = orig }()

	val, ok := resolveToken(envVar, "erg-test/nonexistent")
	if !ok {
		t.Fatal("resolveToken returned ok=false when env var is set")
	}
	if val != "from-env" {
		t.Errorf("resolveToken = %q, want %q", val, "from-env")
	}
}

func TestResolveToken_FallsBackToKeychain(t *testing.T) {
	const envVar = "ERG_TEST_TOKEN_RESOLVE_KC"
	os.Unsetenv(envVar)

	orig := keychainGet
	keychainGet = func(service string) (string, bool) {
		if service == "erg-test/my-service" {
			return "keychain-value", true
		}
		return "", false
	}
	defer func() { keychainGet = orig }()

	val, ok := resolveToken(envVar, "erg-test/my-service")
	if !ok {
		t.Fatal("resolveToken returned ok=false when keychain has value")
	}
	if val != "keychain-value" {
		t.Errorf("resolveToken = %q, want %q", val, "keychain-value")
	}
}

func TestResolveToken_NeitherSet(t *testing.T) {
	const envVar = "ERG_TEST_TOKEN_RESOLVE_EMPTY"
	os.Unsetenv(envVar)
	disableKeychainForTest(t)

	val, ok := resolveToken(envVar, "erg-test/nonexistent")
	if ok {
		t.Errorf("resolveToken returned ok=true with no env var and no keychain entry")
	}
	if val != "" {
		t.Errorf("resolveToken = %q, want empty", val)
	}
}
