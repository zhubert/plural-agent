// Package launchd manages macOS LaunchAgent plists for running erg as a service.
package launchd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/zhubert/erg/internal/paths"
)

const (
	Label    = "com.erg.daemon"
	plistTpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{ .Label }}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{ .ErgPath }}</string>
		<string>start</string>
		<string>-f</string>
		<string>--config</string>
		<string>{{ .ConfigPath }}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>{{ .LogPath }}</string>
	<key>StandardErrorPath</key>
	<string>{{ .LogPath }}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>{{ .Path }}</string>
	</dict>
</dict>
</plist>
`
)

var plistTemplate = template.Must(template.New("plist").Parse(plistTpl))

type plistData struct {
	Label      string
	ErgPath    string
	ConfigPath string
	LogPath    string
	Path       string
}

// PlistPath returns the path where the LaunchAgent plist is installed.
func PlistPath() (string, error) {
	dir, err := paths.LaunchAgentsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, Label+".plist"), nil
}

// ServiceLogPath returns the path to the service stdout/stderr log.
func ServiceLogPath() (string, error) {
	dir, err := paths.LogsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "service.log"), nil
}

// osExecutable is the function used to resolve the current binary path.
// Overridden in tests.
var osExecutable = os.Executable

// Install writes the LaunchAgent plist and loads it with launchctl.
// configPath is the path to the daemon manifest (daemon.yaml).
func Install(configPath string) error {
	plistPath, err := PlistPath()
	if err != nil {
		return fmt.Errorf("failed to determine plist path: %w", err)
	}

	// Ensure the config file exists
	if _, err := os.Stat(configPath); err != nil {
		return fmt.Errorf("config file not found: %s\nRun 'erg configure' first or create %s", configPath, configPath)
	}

	ergPath, err := osExecutable()
	if err != nil {
		return fmt.Errorf("failed to resolve erg binary path: %w", err)
	}

	logPath, err := ServiceLogPath()
	if err != nil {
		return fmt.Errorf("failed to determine log path: %w", err)
	}

	// Ensure log directory exists
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// Ensure LaunchAgents directory exists
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents directory: %w", err)
	}

	data := plistData{
		Label:      Label,
		ErgPath:    ergPath,
		ConfigPath: configPath,
		LogPath:    logPath,
		Path:       os.Getenv("PATH"),
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("failed to create plist file: %w", err)
	}
	defer f.Close()

	if err := plistTemplate.Execute(f, data); err != nil {
		return fmt.Errorf("failed to write plist: %w", err)
	}

	// Load the service
	uid := os.Getuid()
	if err := launchctlExec("bootstrap", fmt.Sprintf("gui/%d", uid), plistPath); err != nil {
		return fmt.Errorf("failed to load service: %w", err)
	}

	return nil
}

// Uninstall stops the service and removes the plist file.
func Uninstall() error {
	plistPath, err := PlistPath()
	if err != nil {
		return err
	}

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return fmt.Errorf("service is not installed (no plist at %s)", plistPath)
	}

	uid := os.Getuid()
	// bootout may fail if not loaded; that's ok
	_ = launchctlExec("bootout", fmt.Sprintf("gui/%d/%s", uid, Label))

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove plist: %w", err)
	}

	return nil
}

// Status returns information about the service state.
type Status struct {
	Installed bool
	Running   bool
	PID       int
	ExitCode  int
	PlistPath string
}

// GetStatus checks whether the service is installed and running.
func GetStatus() (*Status, error) {
	plistPath, err := PlistPath()
	if err != nil {
		return nil, err
	}

	s := &Status{PlistPath: plistPath}

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return s, nil
	}
	s.Installed = true

	// Check launchctl print for the service
	uid := os.Getuid()
	out, err := launchctlOutput("print", fmt.Sprintf("gui/%d/%s", uid, Label))
	if err != nil {
		return s, nil
	}

	// Parse PID and state from output
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if pidStr, ok := strings.CutPrefix(line, "pid = "); ok {
			if pid, err := strconv.Atoi(strings.TrimSpace(pidStr)); err == nil && pid > 0 {
				s.PID = pid
				s.Running = true
			}
		}
		if codeStr, ok := strings.CutPrefix(line, "last exit code = "); ok {
			if code, err := strconv.Atoi(strings.TrimSpace(codeStr)); err == nil {
				s.ExitCode = code
			}
		}
	}

	return s, nil
}

// launchctlExec runs a launchctl command. Overridden in tests.
var launchctlExec = func(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// launchctlOutput runs a launchctl command and returns stdout. Overridden in tests.
var launchctlOutput = func(args ...string) (string, error) {
	out, err := exec.Command("launchctl", args...).Output()
	return string(out), err
}
