package launchd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/paths"
)

func TestPlistPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths.Reset()

	p, err := PlistPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(p, Label+".plist") {
		t.Errorf("expected plist path to end with %s.plist, got %s", Label, p)
	}
	if !strings.Contains(p, "LaunchAgents") {
		t.Errorf("expected plist path to contain LaunchAgents, got %s", p)
	}
}

func TestServiceLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create ~/.erg so legacy layout is used
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	p, err := ServiceLogPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(p, "service.log") {
		t.Errorf("expected log path to end with service.log, got %s", p)
	}
}

func TestInstall_MissingConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Override osExecutable
	origExec := osExecutable
	osExecutable = func() (string, error) { return "/usr/local/bin/erg", nil }
	defer func() { osExecutable = origExec }()

	err := Install("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if !strings.Contains(err.Error(), "config file not found") {
		t.Errorf("expected 'config file not found' error, got: %v", err)
	}
}

func TestInstall_WritesPlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create a fake config file
	configPath := filepath.Join(home, ".erg", "daemon.yaml")
	os.WriteFile(configPath, []byte("repos:\n  - path: /tmp/repo\n"), 0o644)

	// Override osExecutable
	origExec := osExecutable
	osExecutable = func() (string, error) { return "/usr/local/bin/erg", nil }
	defer func() { osExecutable = origExec }()

	// Override launchctlExec to be a no-op
	origLaunchctl := launchctlExec
	launchctlExec = func(args ...string) error { return nil }
	defer func() { launchctlExec = origLaunchctl }()

	if err := Install(configPath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify plist was written
	plistPath, _ := PlistPath()
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("failed to read plist: %v", err)
	}

	plist := string(data)
	if !strings.Contains(plist, Label) {
		t.Errorf("plist missing label %s", Label)
	}
	if !strings.Contains(plist, "/usr/local/bin/erg") {
		t.Errorf("plist missing erg path")
	}
	if !strings.Contains(plist, configPath) {
		t.Errorf("plist missing config path")
	}
	if !strings.Contains(plist, "<key>RunAtLoad</key>") {
		t.Errorf("plist missing RunAtLoad")
	}
	if !strings.Contains(plist, "<key>KeepAlive</key>") {
		t.Errorf("plist missing KeepAlive")
	}
}

func TestUninstall_NotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	err := Uninstall()
	if err == nil {
		t.Fatal("expected error when not installed")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("expected 'not installed' error, got: %v", err)
	}
}

func TestUninstall_RemovesPlist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create a fake plist
	plistPath, _ := PlistPath()
	os.MkdirAll(filepath.Dir(plistPath), 0o755)
	os.WriteFile(plistPath, []byte("fake plist"), 0o644)

	// Override launchctlExec to be a no-op
	origLaunchctl := launchctlExec
	launchctlExec = func(args ...string) error { return nil }
	defer func() { launchctlExec = origLaunchctl }()

	if err := Uninstall(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Error("expected plist to be removed")
	}
}

func TestGetStatus_NotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	status, err := GetStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Installed {
		t.Error("expected Installed=false")
	}
	if status.Running {
		t.Error("expected Running=false")
	}
}

func TestGetStatus_InstalledNotRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create a fake plist
	plistPath, _ := PlistPath()
	os.MkdirAll(filepath.Dir(plistPath), 0o755)
	os.WriteFile(plistPath, []byte("fake plist"), 0o644)

	// Override launchctlOutput to simulate not loaded
	origOutput := launchctlOutput
	launchctlOutput = func(args ...string) (string, error) {
		return "", os.ErrNotExist
	}
	defer func() { launchctlOutput = origOutput }()

	status, err := GetStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Installed {
		t.Error("expected Installed=true")
	}
	if status.Running {
		t.Error("expected Running=false when launchctl fails")
	}
}

func TestGetStatus_Running(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".erg"), 0o755)
	paths.Reset()

	// Create a fake plist
	plistPath, _ := PlistPath()
	os.MkdirAll(filepath.Dir(plistPath), 0o755)
	os.WriteFile(plistPath, []byte("fake plist"), 0o644)

	// Override launchctlOutput to simulate running service
	origOutput := launchctlOutput
	launchctlOutput = func(args ...string) (string, error) {
		return "com.erg.daemon = {\n\tpid = 12345\n\tlast exit code = 0\n}\n", nil
	}
	defer func() { launchctlOutput = origOutput }()

	status, err := GetStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Installed {
		t.Error("expected Installed=true")
	}
	if !status.Running {
		t.Error("expected Running=true")
	}
	if status.PID != 12345 {
		t.Errorf("expected PID=12345, got %d", status.PID)
	}
	if status.ExitCode != 0 {
		t.Errorf("expected ExitCode=0, got %d", status.ExitCode)
	}
}
