package cmd

import (
	"runtime"
	"testing"

	"github.com/spf13/cobra"
)

func TestServicesCmdRegistered(t *testing.T) {
	found := false
	for _, sub := range rootCmd.Commands() {
		if sub.Use == "services" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'services' subcommand to be registered on rootCmd")
	}
}

func TestServicesSubcommands(t *testing.T) {
	expected := map[string]bool{
		"install":   false,
		"uninstall": false,
		"status":    false,
		"log":       false,
	}

	for _, sub := range servicesCmd.Commands() {
		if _, ok := expected[sub.Use]; ok {
			expected[sub.Use] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("expected '%s' subcommand on services", name)
		}
	}
}

func TestServicesInstall_NonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("test only runs on non-darwin")
	}
	err := runServicesInstall(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("expected error on non-darwin")
	}
}

func TestServicesInstallConfigFlag(t *testing.T) {
	flag := servicesInstallCmd.Flags().Lookup("config")
	if flag == nil {
		t.Fatal("expected --config flag on services install")
	}
}
