package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/cli"
)

// allFoundChecker returns a checker where every prerequisite is found.
func allFoundChecker(prereqs []cli.Prerequisite) []cli.CheckResult {
	results := make([]cli.CheckResult, len(prereqs))
	for i, p := range prereqs {
		results[i] = cli.CheckResult{
			Prerequisite: p,
			Found:        true,
			Version:      p.Name + " version 1.0",
		}
	}
	return results
}

// noneFoundChecker returns a checker where no prerequisite is found.
func noneFoundChecker(prereqs []cli.Prerequisite) []cli.CheckResult {
	results := make([]cli.CheckResult, len(prereqs))
	for i, p := range prereqs {
		results[i] = cli.CheckResult{
			Prerequisite: p,
			Found:        false,
		}
	}
	return results
}

// partialChecker returns a checker with specific tools found by name.
func partialChecker(found map[string]bool) prereqCheckerFn {
	return func(prereqs []cli.Prerequisite) []cli.CheckResult {
		results := make([]cli.CheckResult, len(prereqs))
		for i, p := range prereqs {
			results[i] = cli.CheckResult{
				Prerequisite: p,
				Found:        found[p.Name],
			}
			if found[p.Name] {
				results[i].Version = p.Name + " version 1.0"
			}
		}
		return results
	}
}

func TestRunSetup_AllPrereqsMissing(t *testing.T) {
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader(""), &out, noneFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Some required tools are missing") {
		t.Errorf("expected missing tools message, got:\n%s", output)
	}
	if !strings.Contains(output, "erg setup") {
		t.Errorf("expected retry instruction, got:\n%s", output)
	}
	// Should not reach the issue tracker prompt
	if strings.Contains(output, "Which issue tracker") {
		t.Errorf("should not show issue tracker prompt when prereqs are missing, got:\n%s", output)
	}
}

func TestRunSetup_RequiredMissing_ShowsInstallURLs(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": false, "gh": true})
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader(""), &out, checker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "https://git-scm.com") {
		t.Errorf("expected git install URL, got:\n%s", output)
	}
	if !strings.Contains(output, "https://claude.ai/code") {
		t.Errorf("expected claude install URL, got:\n%s", output)
	}
}

func TestRunSetup_AllPresent_ShowsTrackerMenu(t *testing.T) {
	var out bytes.Buffer
	// Provide no input — will get invalid choice, but we just want to see the menu
	err := runSetupWithIO(strings.NewReader("\n"), &out, allFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "All prerequisites installed") {
		t.Errorf("expected success message, got:\n%s", output)
	}
	if !strings.Contains(output, "GitHub Issues") {
		t.Errorf("expected GitHub option in menu, got:\n%s", output)
	}
	if !strings.Contains(output, "Asana Tasks") {
		t.Errorf("expected Asana option in menu, got:\n%s", output)
	}
	if !strings.Contains(output, "Linear Issues") {
		t.Errorf("expected Linear option in menu, got:\n%s", output)
	}
}

func TestRunSetup_GitHub(t *testing.T) {
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader("1\n"), &out, allFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "GitHub Issues Setup") {
		t.Errorf("expected GitHub setup header, got:\n%s", output)
	}
	if !strings.Contains(output, "gh auth login") {
		t.Errorf("expected gh auth login instruction, got:\n%s", output)
	}
	if !strings.Contains(output, "queued") {
		t.Errorf("expected queued label mention, got:\n%s", output)
	}
	if !strings.Contains(output, "erg workflow init") {
		t.Errorf("expected workflow init instruction, got:\n%s", output)
	}
}

func TestRunSetup_Asana(t *testing.T) {
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader("2\n"), &out, allFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Asana Tasks Setup") {
		t.Errorf("expected Asana setup header, got:\n%s", output)
	}
	if !strings.Contains(output, "ASANA_PAT") {
		t.Errorf("expected ASANA_PAT env var mention, got:\n%s", output)
	}
	if !strings.Contains(output, "app.asana.com") {
		t.Errorf("expected Asana URL, got:\n%s", output)
	}
	if !strings.Contains(output, "provider: asana") {
		t.Errorf("expected workflow YAML snippet, got:\n%s", output)
	}
	if !strings.Contains(output, "PROJECT_GID") {
		t.Errorf("expected project GID placeholder, got:\n%s", output)
	}
}

func TestRunSetup_Linear(t *testing.T) {
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader("3\n"), &out, allFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Linear Issues Setup") {
		t.Errorf("expected Linear setup header, got:\n%s", output)
	}
	if !strings.Contains(output, "LINEAR_API_KEY") {
		t.Errorf("expected LINEAR_API_KEY env var mention, got:\n%s", output)
	}
	if !strings.Contains(output, "linear.app") {
		t.Errorf("expected Linear URL, got:\n%s", output)
	}
	if !strings.Contains(output, "provider: linear") {
		t.Errorf("expected workflow YAML snippet, got:\n%s", output)
	}
	if !strings.Contains(output, "TEAM_ID") {
		t.Errorf("expected team ID placeholder, got:\n%s", output)
	}
}

func TestRunSetup_InvalidChoice(t *testing.T) {
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader("5\n"), &out, allFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Invalid choice") {
		t.Errorf("expected invalid choice message, got:\n%s", output)
	}
}

func TestRunSetup_EmptyInput(t *testing.T) {
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader(""), &out, allFoundChecker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Invalid choice") {
		t.Errorf("expected invalid choice message for empty input, got:\n%s", output)
	}
}

func TestRunSetup_PrereqStatusSymbols(t *testing.T) {
	// git found (required), claude found (required), gh not found (optional)
	checker := partialChecker(map[string]bool{"git": true, "claude": true, "gh": false})
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader("\n"), &out, checker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "✓ git") {
		t.Errorf("expected ✓ for git, got:\n%s", output)
	}
	if !strings.Contains(output, "✓ claude") {
		t.Errorf("expected ✓ for claude, got:\n%s", output)
	}
	// gh is optional and not found — should show ○
	if !strings.Contains(output, "○ gh") {
		t.Errorf("expected ○ for optional gh, got:\n%s", output)
	}
	// All required tools present, so should proceed to tracker menu
	if !strings.Contains(output, "All prerequisites installed") {
		t.Errorf("expected success message when required tools present, got:\n%s", output)
	}
}

func TestRunSetup_RequiredMissingSymbol(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": true, "gh": true})
	var out bytes.Buffer
	err := runSetupWithIO(strings.NewReader(""), &out, checker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "✗ git") {
		t.Errorf("expected ✗ for missing required git, got:\n%s", output)
	}
}
