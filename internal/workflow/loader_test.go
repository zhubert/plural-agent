package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_FileNotExists(t *testing.T) {
	cfg, err := Load("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
workflow: test-flow
start: coding

source:
  provider: github
  filter:
    label: "ready"

states:
  coding:
    type: task
    action: ai.code
    params:
      max_turns: 25
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Source.Provider != "github" {
		t.Errorf("provider: got %q, want github", cfg.Source.Provider)
	}
	if cfg.Source.Filter.Label != "ready" {
		t.Errorf("label: got %q, want ready", cfg.Source.Filter.Label)
	}
	if cfg.Start != "coding" {
		t.Errorf("start: got %q", cfg.Start)
	}
	coding := cfg.States["coding"]
	if coding == nil {
		t.Fatal("expected coding state")
	}
	p := NewParamHelper(coding.Params)
	if p.Int("max_turns", 0) != 25 {
		t.Error("max_turns: expected 25")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte("{{invalid yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_OldFormatDetection(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Old format has workflow as a nested struct
	oldYAML := `
source:
  provider: github
  filter:
    label: "queued"
workflow:
  coding:
    max_turns: 50
  merge:
    method: rebase
`
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte(oldYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for old format")
	}
	if !strings.Contains(err.Error(), "old flat format") {
		t.Errorf("expected old format error message, got: %v", err)
	}
}

func TestLoad_SourceOnlyNotOldFormat(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// A config with only "source" (no "states" key, no "workflow" map)
	// should NOT be detected as old format — it's incomplete new format.
	sourceOnlyYAML := `
source:
  provider: github
  filter:
    label: "queued"
`
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte(sourceOnlyYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("source-only config should not error as old format: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Source.Provider != "github" {
		t.Errorf("provider: got %q, want github", cfg.Source.Provider)
	}
}

func TestLoadAndMerge_NoFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadAndMerge(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config when no workflow file exists")
	}
}

func TestLoad_SettingsBlock(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	autoMerge := false
	_ = autoMerge // used in yaml below

	yamlContent := `
workflow: test-flow
start: coding

source:
  provider: github
  filter:
    label: queued

settings:
  max_turns: 80
  max_duration: 45
  auto_merge: false
  merge_method: squash
  max_concurrent: 5
  container_image: custom-image:latest
  branch_prefix: agent/

states:
  coding:
    type: task
    action: ai.code
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Settings == nil {
		t.Fatal("expected non-nil settings")
	}
	if cfg.Settings.MaxTurns != 80 {
		t.Errorf("MaxTurns: got %d, want 80", cfg.Settings.MaxTurns)
	}
	if cfg.Settings.MaxDuration != 45 {
		t.Errorf("MaxDuration: got %d, want 45", cfg.Settings.MaxDuration)
	}
	if cfg.Settings.AutoMerge == nil {
		t.Fatal("AutoMerge: expected non-nil")
	}
	if *cfg.Settings.AutoMerge != false {
		t.Errorf("AutoMerge: got %v, want false", *cfg.Settings.AutoMerge)
	}
	if cfg.Settings.MergeMethod != "squash" {
		t.Errorf("MergeMethod: got %q, want squash", cfg.Settings.MergeMethod)
	}
	if cfg.Settings.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent: got %d, want 5", cfg.Settings.MaxConcurrent)
	}
	if cfg.Settings.ContainerImage != "custom-image:latest" {
		t.Errorf("ContainerImage: got %q, want custom-image:latest", cfg.Settings.ContainerImage)
	}
	if cfg.Settings.BranchPrefix != "agent/" {
		t.Errorf("BranchPrefix: got %q, want agent/", cfg.Settings.BranchPrefix)
	}
}

func TestLoadFile_NotExists(t *testing.T) {
	cfg, err := LoadFile("/nonexistent/path/workflow.yaml")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config for missing file")
	}
}

func TestLoadFile_ValidFile(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `
workflow: test-flow
start: coding

source:
  provider: github
  filter:
    label: "ready"

states:
  coding:
    type: task
    action: ai.code
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	fp := filepath.Join(dir, "my-workflow.yaml")
	if err := os.WriteFile(fp, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Source.Provider != "github" {
		t.Errorf("provider: got %q, want github", cfg.Source.Provider)
	}
	if cfg.Source.Filter.Label != "ready" {
		t.Errorf("label: got %q, want ready", cfg.Source.Filter.Label)
	}
}

func TestLoadFile_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(fp, []byte("{{invalid yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(fp)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoadAndMergeWithFile_UsesExplicitPath(t *testing.T) {
	// Write a workflow file to a non-default location.
	dir := t.TempDir()
	yamlContent := `
workflow: custom-flow
start: coding

source:
  provider: linear
  filter:
    team: "custom-team"

states:
  coding:
    type: task
    action: ai.code
    next: done
    error: failed
  done:
    type: succeed
  failed:
    type: fail
`
	customFile := filepath.Join(dir, "custom-workflow.yaml")
	if err := os.WriteFile(customFile, []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// repoPath points to an empty dir (no .erg/workflow.yaml) — proves the
	// explicit file path is used rather than the default location.
	emptyRepo := t.TempDir()
	cfg, err := LoadAndMergeWithFile(emptyRepo, customFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Source.Provider != "linear" {
		t.Errorf("provider: got %q, want linear", cfg.Source.Provider)
	}
	if cfg.Source.Filter.Team != "custom-team" {
		t.Errorf("team: got %q, want custom-team", cfg.Source.Filter.Team)
	}
}

func TestLoadAndMergeWithFile_EmptyPathNoFile(t *testing.T) {
	// With an empty workflowFile and no .erg/workflow.yaml, returns nil.
	dir := t.TempDir()
	cfg, err := LoadAndMergeWithFile(dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config when no workflow file exists")
	}
}

func TestLoadAndMerge_PartialFile(t *testing.T) {
	dir := t.TempDir()
	ergDir := filepath.Join(dir, ".erg")
	if err := os.MkdirAll(ergDir, 0o755); err != nil {
		t.Fatal(err)
	}

	yamlContent := `
source:
  provider: linear
  filter:
    team: "my-team"

states:
  merge:
    type: task
    action: github.merge
    params:
      method: squash
    next: done
`
	if err := os.WriteFile(filepath.Join(ergDir, "workflow.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadAndMerge(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Explicit values preserved
	if cfg.Source.Provider != "linear" {
		t.Errorf("provider: got %q, want linear", cfg.Source.Provider)
	}
	mp := NewParamHelper(cfg.States["merge"].Params)
	if mp.String("method", "") != "squash" {
		t.Errorf("merge method: got %q", mp.String("method", ""))
	}

	// Terminal states provided by base
	if cfg.States["done"] == nil {
		t.Fatal("expected done state from base")
	}
	if cfg.States["failed"] == nil {
		t.Fatal("expected failed state from base")
	}
}
