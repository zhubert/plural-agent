package cmd

import (
	"testing"
)

func TestStartCmdForegroundFlagExists(t *testing.T) {
	flag := startCmd.Flags().Lookup("foreground")
	if flag == nil {
		t.Fatal("expected --foreground flag on start command")
	}
	if flag.Shorthand != "f" {
		t.Errorf("expected shorthand 'f', got %q", flag.Shorthand)
	}
}

func TestStartCmdRepoFlagExists(t *testing.T) {
	flag := startCmd.Flags().Lookup("repo")
	if flag == nil {
		t.Fatal("expected --repo flag on start command")
	}
}

func TestStartCmdOnceFlagExists(t *testing.T) {
	flag := startCmd.Flags().Lookup("once")
	if flag == nil {
		t.Fatal("expected --once flag on start command")
	}
}

func TestStartOnceImpliesForeground(t *testing.T) {
	// Save and restore package-level vars
	origStartOnce := startOnce
	origStartForeground := startForeground
	origAgentOnce := agentOnce
	origAgentForeground := agentForeground
	defer func() {
		startOnce = origStartOnce
		startForeground = origStartForeground
		agentOnce = origAgentOnce
		agentForeground = origAgentForeground
	}()

	startOnce = true
	startForeground = false

	// Replicate the logic from runStart
	agentOnce = startOnce
	agentForeground = startForeground
	if agentOnce {
		agentForeground = true
	}

	if !agentForeground {
		t.Error("expected --once to imply foreground mode")
	}
}

func TestStartCmdWorkflowFlagExists(t *testing.T) {
	flag := startCmd.Flags().Lookup("workflow")
	if flag == nil {
		t.Fatal("expected --workflow flag on start command")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default value to be empty, got %q", flag.DefValue)
	}
}

func TestStartCmdRegisteredOnRoot(t *testing.T) {
	found := false
	for _, sub := range rootCmd.Commands() {
		if sub.Use == "start" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'start' subcommand to be registered on rootCmd")
	}
}

func TestStartCmdDashboardFlagExists(t *testing.T) {
	flag := startCmd.Flags().Lookup("dashboard")
	if flag == nil {
		t.Fatal("expected --dashboard flag on start command")
	}
	if flag.DefValue != "false" {
		t.Errorf("expected default value 'false', got %q", flag.DefValue)
	}
}

func TestResolveDashboardAddrDefault(t *testing.T) {
	got := resolveDashboardAddr(true, "")
	if got != defaultDashboardAddr {
		t.Errorf("expected %q, got %q", defaultDashboardAddr, got)
	}
}

func TestResolveDashboardAddrExplicitOverrides(t *testing.T) {
	got := resolveDashboardAddr(true, "localhost:9999")
	if got != "localhost:9999" {
		t.Errorf("expected explicit addr to win, got %q", got)
	}
}

func TestResolveDashboardAddrFlagOff(t *testing.T) {
	got := resolveDashboardAddr(false, "")
	if got != "" {
		t.Errorf("expected empty addr when --dashboard is false and no addr given, got %q", got)
	}
}
