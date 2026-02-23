package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowInitCmd_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	workflowRepoPath = dir

	err := workflowInitCmd.RunE(workflowInitCmd, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fp := filepath.Join(dir, ".erg", "workflow.yaml")
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		t.Fatal("expected workflow.yaml to be created")
	}
}

func TestWorkflowInitCmd_ErrorsIfExists(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	workflowRepoPath = dir

	err := workflowInitCmd.RunE(workflowInitCmd, []string{})
	if err == nil {
		t.Fatal("expected error when file already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should mention 'already exists': %v", err)
	}
}
