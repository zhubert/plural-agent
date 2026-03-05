package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/zhubert/erg/internal/launchd"
	"github.com/zhubert/erg/internal/paths"
)

var servicesCmd = &cobra.Command{
	Use:     "services",
	Short:   "Manage erg as a system service (macOS launchd)",
	GroupID: "daemon",
	Long: `Manage erg as a macOS LaunchAgent service that starts automatically on login.

Requires a daemon config at ~/.erg/daemon.yaml (or XDG equivalent).
Run 'erg configure' first to set up your repos, then 'erg services install'.

Examples:
  erg services install     # Install and start the LaunchAgent
  erg services uninstall   # Stop and remove the LaunchAgent
  erg services status      # Show service status
  erg services log         # Tail the service log`,
}

var servicesInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and start the LaunchAgent",
	Long: `Writes a LaunchAgent plist and loads it with launchctl.
The service will start automatically on login and restart on failure.

Uses the daemon config at ~/.erg/daemon.yaml by default.`,
	RunE: runServicesInstall,
}

var servicesUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the LaunchAgent",
	RunE:  runServicesUninstall,
}

var servicesStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show service status",
	RunE:  runServicesStatus,
}

var servicesLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Tail the service log",
	RunE:  runServicesLog,
}

var servicesConfigPath string

func init() {
	servicesInstallCmd.Flags().StringVar(&servicesConfigPath, "config", "", "Path to daemon config file (default: ~/.erg/daemon.yaml)")
	servicesCmd.AddCommand(servicesInstallCmd)
	servicesCmd.AddCommand(servicesUninstallCmd)
	servicesCmd.AddCommand(servicesStatusCmd)
	servicesCmd.AddCommand(servicesLogCmd)
	rootCmd.AddCommand(servicesCmd)
}

func runServicesInstall(_ *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("services command is only supported on macOS")
	}

	configPath := servicesConfigPath
	if configPath == "" {
		p, err := paths.ManifestPath()
		if err != nil {
			return fmt.Errorf("failed to determine default config path: %w", err)
		}
		configPath = p
	}

	if err := launchd.Install(configPath); err != nil {
		return err
	}

	fmt.Printf("Service installed and started.\n")
	fmt.Printf("  Config: %s\n", configPath)

	logPath, _ := launchd.ServiceLogPath()
	fmt.Printf("  Log:    %s\n", logPath)

	plistPath, _ := launchd.PlistPath()
	fmt.Printf("  Plist:  %s\n", plistPath)
	fmt.Println("\nThe service will start automatically on login.")
	fmt.Println("Use 'erg services status' to check on it.")
	return nil
}

func runServicesUninstall(_ *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("services command is only supported on macOS")
	}

	if err := launchd.Uninstall(); err != nil {
		return err
	}

	fmt.Println("Service uninstalled.")
	return nil
}

func runServicesStatus(_ *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("services command is only supported on macOS")
	}

	status, err := launchd.GetStatus()
	if err != nil {
		return err
	}

	if !status.Installed {
		fmt.Println("Service: not installed")
		fmt.Println("\nRun 'erg services install' to set up the LaunchAgent.")
		return nil
	}

	if status.Running {
		fmt.Printf("Service: running (PID %d)\n", status.PID)
	} else {
		fmt.Println("Service: stopped")
		if status.ExitCode != 0 {
			fmt.Printf("  Last exit code: %d\n", status.ExitCode)
		}
	}
	fmt.Printf("Plist:   %s\n", status.PlistPath)

	logPath, _ := launchd.ServiceLogPath()
	fmt.Printf("Log:     %s\n", logPath)

	return nil
}

// execCommandFunc is the function used to run tail. Overridden in tests.
var execCommandFunc = execCommandDefault

func execCommandDefault(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runServicesLog(_ *cobra.Command, _ []string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("services command is only supported on macOS")
	}

	logPath, err := launchd.ServiceLogPath()
	if err != nil {
		return err
	}

	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return fmt.Errorf("no service log found at %s\nIs the service running? Check with 'erg services status'", logPath)
	}

	fmt.Printf("Tailing %s (Ctrl+C to stop)\n\n", logPath)
	return execCommandFunc("tail", "-f", logPath)
}
