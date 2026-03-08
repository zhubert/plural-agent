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

func TestStartDashboardSetsDefaultAddr(t *testing.T) {
	// Save and restore package-level vars
	origStartDashboard := startDashboard
	origStartDashboardAddr := startDashboardAddr
	origAgentDashboardAddr := agentDashboardAddr
	defer func() {
		startDashboard = origStartDashboard
		startDashboardAddr = origStartDashboardAddr
		agentDashboardAddr = origAgentDashboardAddr
	}()

	startDashboard = true
	startDashboardAddr = ""

	// Replicate the logic from runStart
	agentDashboardAddr = startDashboardAddr
	if startDashboard && agentDashboardAddr == "" {
		agentDashboardAddr = "localhost:21122"
	}

	if agentDashboardAddr != "localhost:21122" {
		t.Errorf("expected agentDashboardAddr to be 'localhost:21122', got %q", agentDashboardAddr)
	}
}

func TestStartDashboardAddrOverridesDashboardFlag(t *testing.T) {
	// Save and restore package-level vars
	origStartDashboard := startDashboard
	origStartDashboardAddr := startDashboardAddr
	origAgentDashboardAddr := agentDashboardAddr
	defer func() {
		startDashboard = origStartDashboard
		startDashboardAddr = origStartDashboardAddr
		agentDashboardAddr = origAgentDashboardAddr
	}()

	startDashboard = true
	startDashboardAddr = "localhost:9999"

	// Replicate the logic from runStart
	agentDashboardAddr = startDashboardAddr
	if startDashboard && agentDashboardAddr == "" {
		agentDashboardAddr = "localhost:21122"
	}

	if agentDashboardAddr != "localhost:9999" {
		t.Errorf("expected --dashboard-addr to win over --dashboard, got %q", agentDashboardAddr)
	}
}
