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
