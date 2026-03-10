package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/secrets"
	"github.com/zhubert/erg/internal/workflow"
)

// keychainSkip returns "n\n" on macOS (to decline the keychain prompt) or ""
// on other platforms where the prompt doesn't appear.
func keychainSkip() string {
	if secrets.IsKeychainAvailable() {
		return "n\n"
	}
	return ""
}

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

// noopWriter is a workflowWriterFn that succeeds without writing any files.
func noopWriter(repoPath string, cfg workflow.WizardConfig) (string, error) {
	return filepath.Join(repoPath, ".erg", "workflow.yaml"), nil
}

// captureWriter returns a workflowWriterFn that stores the last WizardConfig passed to it.
func captureWriter(captured *workflow.WizardConfig) workflowWriterFn {
	return func(repoPath string, cfg workflow.WizardConfig) (string, error) {
		*captured = cfg
		return filepath.Join(repoPath, ".erg", "workflow.yaml"), nil
	}
}

// githubDefaultInput: tracker=1(GitHub), label=(default), plan=n, reviewer=(skip),
// automerge=y(default), merge=1(rebase), containers=n, confirm=y
const githubDefaultInput = "1\n\nn\n\ny\n1\nn\ny\n"

func TestRunConfigure_AllPrereqsMissing(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, noneFoundChecker, ".", noopWriter, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Some required tools are missing") {
		t.Errorf("expected missing tools message, got:\n%s", output)
	}
	if !strings.Contains(output, "erg configure") {
		t.Errorf("expected retry instruction, got:\n%s", output)
	}
	if strings.Contains(output, "issue tracker") {
		t.Errorf("should not show issue tracker prompt when prereqs are missing, got:\n%s", output)
	}
}

func TestRunConfigure_RequiredMissing_ShowsInstallURLs(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": false, "gh": true})
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, checker, ".", noopWriter, true)
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

func TestRunConfigure_GitHub(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(githubDefaultInput), &out, allFoundChecker, ".", noopWriter, true)
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
	if !strings.Contains(output, "workflow.yaml") {
		t.Errorf("expected workflow.yaml mention, got:\n%s", output)
	}
	if !strings.Contains(output, "erg start") {
		t.Errorf("expected erg start instruction, got:\n%s", output)
	}
}

func TestRunConfigure_GitHub_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// tracker=1(GitHub), label=myqueue, plan=y, reviewer=alice,
	// automerge=y, method=2(squash), containers=y, confirm=y
	input := "1\nmyqueue\ny\nalice\ny\n2\ny\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Provider != "github" {
		t.Errorf("expected provider github, got %q", captured.Provider)
	}
	if captured.Label != "myqueue" {
		t.Errorf("expected label myqueue, got %q", captured.Label)
	}
	if !captured.PlanFirst {
		t.Errorf("expected PlanFirst=true")
	}
	if captured.Reviewer != "alice" {
		t.Errorf("expected Reviewer=alice, got %q", captured.Reviewer)
	}
	if !captured.AutoMerge {
		t.Errorf("expected AutoMerge=true")
	}
	if captured.MergeMethod != "squash" {
		t.Errorf("expected MergeMethod=squash, got %q", captured.MergeMethod)
	}
	if !captured.Containerized {
		t.Errorf("expected Containerized=true")
	}
}

func TestRunConfigure_GitHub_WizardDefaults(t *testing.T) {
	var captured workflow.WizardConfig
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(githubDefaultInput), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Provider != "github" {
		t.Errorf("expected provider github, got %q", captured.Provider)
	}
	if captured.Label != "queued" {
		t.Errorf("expected default label queued, got %q", captured.Label)
	}
	if captured.PlanFirst {
		t.Errorf("expected PlanFirst=false (default)")
	}
	if !captured.AutoMerge {
		t.Errorf("expected AutoMerge=true (default)")
	}
	if captured.MergeMethod != "rebase" {
		t.Errorf("expected MergeMethod=rebase (default), got %q", captured.MergeMethod)
	}
}

func TestRunConfigure_GitHub_AutoMerge_False(t *testing.T) {
	var captured workflow.WizardConfig
	// tracker=1(GitHub), label=(default), plan=n, reviewer=(skip),
	// automerge=n, containers=n, confirm=y
	input := "1\n\nn\n\nn\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.AutoMerge {
		t.Errorf("expected AutoMerge=false")
	}
	// Summary should show Auto-merge: false
	output := out.String()
	if !strings.Contains(output, "Auto-merge") {
		t.Errorf("expected Auto-merge in summary, got:\n%s", output)
	}
}

func TestRunConfigure_Asana(t *testing.T) {
	// tracker=2(Asana), [keychain=n on macOS], project=proj123, org=1(tags), tag=(default),
	// section=(skip), completion=(skip), plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=y
	input := "2\n" + keychainSkip() + "proj123\n1\n\n\n\nn\n\ny\n1\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", noopWriter, true)
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
	if !strings.Contains(output, "app.asana.com/0/my-apps") {
		t.Errorf("expected Asana Developer Console URL, got:\n%s", output)
	}
}

func TestRunConfigure_Asana_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// tracker=2(Asana), [keychain=n on macOS], project=1234, org=1(tags), tag=ready,
	// section=(skip), completion=Done, plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=y
	input := "2\n" + keychainSkip() + "1234\n1\nready\n\nDone\nn\n\ny\n1\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Provider != "asana" {
		t.Errorf("expected provider asana, got %q", captured.Provider)
	}
	if captured.Project != "1234" {
		t.Errorf("expected project 1234, got %q", captured.Project)
	}
	if captured.Label != "ready" {
		t.Errorf("expected label ready, got %q", captured.Label)
	}
	if captured.CompletionSection != "Done" {
		t.Errorf("expected CompletionSection=Done, got %q", captured.CompletionSection)
	}
	if captured.Kanban {
		t.Errorf("expected Kanban=false for tags mode")
	}
}

func TestRunConfigure_Linear(t *testing.T) {
	// tracker=3(Linear), [keychain=n on macOS], team=team1, org=1(labels), label=(default),
	// completionState=(skip), plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=y
	input := "3\n" + keychainSkip() + "team1\n1\n\n\nn\n\ny\n1\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", noopWriter, true)
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
}

func TestRunConfigure_Linear_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// tracker=3(Linear), [keychain=n on macOS], team=team-xyz, org=1(labels), label=(default),
	// completionState=Merged, plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=y
	input := "3\n" + keychainSkip() + "team-xyz\n1\n\nMerged\nn\n\ny\n1\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Provider != "linear" {
		t.Errorf("expected provider linear, got %q", captured.Provider)
	}
	if captured.Team != "team-xyz" {
		t.Errorf("expected team team-xyz, got %q", captured.Team)
	}
	if captured.Label != "queued" {
		t.Errorf("expected default label queued, got %q", captured.Label)
	}
	if captured.CompletionState != "Merged" {
		t.Errorf("expected CompletionState=Merged, got %q", captured.CompletionState)
	}
	if captured.Kanban {
		t.Errorf("expected Kanban=false for labels mode")
	}
}

func TestRunConfigure_PrereqStatusSymbols(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": true, "claude": true, "gh": false})
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(githubDefaultInput), &out, checker, ".", noopWriter, true)
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
	if !strings.Contains(output, "○ gh") {
		t.Errorf("expected ○ for optional gh, got:\n%s", output)
	}
	if !strings.Contains(output, "All prerequisites installed") {
		t.Errorf("expected success message when required tools present, got:\n%s", output)
	}
}

func TestRunConfigure_RequiredMissingSymbol(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": true, "gh": true})
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, checker, ".", noopWriter, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "✗ git") {
		t.Errorf("expected ✗ for missing required git, got:\n%s", output)
	}
}

func TestRunConfigure_WorkflowFileAlreadyExists(t *testing.T) {
	existsWriter := func(repoPath string, cfg workflow.WizardConfig) (string, error) {
		fp := filepath.Join(repoPath, ".erg", "workflow.yaml")
		return fp, &alreadyExistsError{fp: fp}
	}

	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(githubDefaultInput), &out, allFoundChecker, ".", existsWriter, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "already exists") {
		t.Errorf("expected 'already exists' message, got:\n%s", output)
	}
	if !strings.Contains(output, "erg start") {
		t.Errorf("expected erg start instruction even when file exists, got:\n%s", output)
	}
}

func TestRunConfigure_ShowsErgStart(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(githubDefaultInput), &out, allFoundChecker, ".", noopWriter, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "erg start") {
		t.Errorf("expected 'erg start' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Setup complete") {
		t.Errorf("expected 'Setup complete' in output, got:\n%s", output)
	}
}

func TestRunConfigure_Asana_Kanban_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// tracker=2(Asana), [keychain=n on macOS], project=1234, org=2(kanban), section=(default "To do"),
	// completion=(default Done), plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=y
	input := "2\n" + keychainSkip() + "1234\n2\n\n\nn\n\ny\n1\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Provider != "asana" {
		t.Errorf("expected provider asana, got %q", captured.Provider)
	}
	if !captured.Kanban {
		t.Errorf("expected Kanban=true")
	}
	if captured.Section != "To do" {
		t.Errorf("expected Section='To do', got %q", captured.Section)
	}
	if captured.Label != "" {
		t.Errorf("expected Label empty for kanban, got %q", captured.Label)
	}
	if captured.CompletionSection != "Done" {
		t.Errorf("expected CompletionSection=Done, got %q", captured.CompletionSection)
	}
}

func TestRunConfigure_Linear_Kanban_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// tracker=3(Linear), [keychain=n on macOS], team=team-xyz, org=2(kanban), label=(default "queued"),
	// completionState=(default Done), plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=y
	input := "3\n" + keychainSkip() + "team-xyz\n2\n\n\nn\n\ny\n1\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Provider != "linear" {
		t.Errorf("expected provider linear, got %q", captured.Provider)
	}
	if !captured.Kanban {
		t.Errorf("expected Kanban=true")
	}
	if captured.Label != "queued" {
		t.Errorf("expected Label=queued for Linear kanban, got %q", captured.Label)
	}
	if captured.CompletionState != "Done" {
		t.Errorf("expected CompletionState=Done, got %q", captured.CompletionState)
	}
}

func TestRunConfigure_ConfirmCancelled(t *testing.T) {
	// tracker=1(GitHub), label=(default), plan=n, reviewer=(skip),
	// automerge=y, merge=1(rebase), containers=n, confirm=n
	input := "1\n\nn\n\ny\n1\nn\nn\n"
	var out bytes.Buffer
	writerCalled := false
	neverWriter := func(repoPath string, cfg workflow.WizardConfig) (string, error) {
		writerCalled = true
		return "", nil
	}
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", neverWriter, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if writerCalled {
		t.Errorf("expected writer NOT to be called when confirmation is declined")
	}
	output := out.String()
	if !strings.Contains(output, "Configuration cancelled") {
		t.Errorf("expected cancellation message, got:\n%s", output)
	}
	if strings.Contains(output, "erg start") {
		t.Errorf("should not show erg start when cancelled, got:\n%s", output)
	}
}

// alreadyExistsError simulates a "file already exists" error from WriteFromWizard.
type alreadyExistsError struct {
	fp string
}

func (e *alreadyExistsError) Error() string {
	return e.fp + " already exists"
}
