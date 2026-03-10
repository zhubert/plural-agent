package secrets

import (
	"runtime"
	"testing"
)

func TestIsKeychainAvailable(t *testing.T) {
	got := IsKeychainAvailable()
	want := runtime.GOOS == "darwin"
	if got != want {
		t.Errorf("IsKeychainAvailable() = %v, want %v", got, want)
	}
}

func TestGetNotFound(t *testing.T) {
	val, ok := Get("erg-test/nonexistent-service-abc123")
	if ok {
		t.Errorf("Get() returned ok=true for nonexistent service")
	}
	if val != "" {
		t.Errorf("Get() returned non-empty value for nonexistent service: %q", val)
	}
}

func TestSetGetDelete(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain tests only run on macOS")
	}

	service := "erg-test/unit-test-secret"
	value := "test-secret-value-12345"

	// Clean up in case of previous failed test
	_ = Delete(service)

	// Set
	if err := Set(service, value); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	// Get
	got, ok := Get(service)
	if !ok {
		t.Fatal("Get() returned ok=false after Set()")
	}
	if got != value {
		t.Errorf("Get() = %q, want %q", got, value)
	}

	// Update
	newValue := "updated-value-67890"
	if err := Set(service, newValue); err != nil {
		t.Fatalf("Set() update error: %v", err)
	}
	got, ok = Get(service)
	if !ok {
		t.Fatal("Get() returned ok=false after update")
	}
	if got != newValue {
		t.Errorf("Get() after update = %q, want %q", got, newValue)
	}

	// Delete
	if err := Delete(service); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	_, ok = Get(service)
	if ok {
		t.Error("Get() returned ok=true after Delete()")
	}
}

func TestSetNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("this test only runs on non-macOS")
	}
	err := Set("erg-test/foo", "bar")
	if err == nil {
		t.Error("Set() should return error on non-macOS")
	}
}
