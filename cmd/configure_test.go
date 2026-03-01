package cmd

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/workflow"
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

func TestRunConfigure_AllPrereqsMissing(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, noneFoundChecker, ".", noopWriter)
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
	// Should not reach the issue tracker prompt
	if strings.Contains(output, "Which issue tracker") {
		t.Errorf("should not show issue tracker prompt when prereqs are missing, got:\n%s", output)
	}
}

func TestRunConfigure_RequiredMissing_ShowsInstallURLs(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": false, "gh": true})
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, checker, ".", noopWriter)
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

func TestRunConfigure_AllPresent_ShowsTrackerMenu(t *testing.T) {
	var out bytes.Buffer
	// Provide no input — will get invalid choice, but we just want to see the menu
	err := runConfigureWithIO(strings.NewReader("\n"), &out, allFoundChecker, ".", noopWriter)
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

func TestRunConfigure_GitHub(t *testing.T) {
	var out bytes.Buffer
	// Input: tracker choice "1", then wizard defaults (all empty → use defaults)
	err := runConfigureWithIO(strings.NewReader("1\n"), &out, allFoundChecker, ".", noopWriter)
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
	if !strings.Contains(output, "Workflow Configuration") {
		t.Errorf("expected workflow wizard section, got:\n%s", output)
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
	// Input: tracker=1, label=myqueue, plan=y, fixci=n, autoreview=n, reviewer=alice, method=squash, containers=y, slack=n
	input := "1\nmyqueue\ny\nn\nn\nalice\nsquash\ny\nn\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured))
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
	if captured.FixCI {
		t.Errorf("expected FixCI=false")
	}
	if captured.AutoReview {
		t.Errorf("expected AutoReview=false")
	}
	if captured.Reviewer != "alice" {
		t.Errorf("expected Reviewer=alice, got %q", captured.Reviewer)
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
	// Input: just tracker choice; wizard gets all defaults via EOF
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader("1\n"), &out, allFoundChecker, ".", captureWriter(&captured))
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
	if !captured.FixCI {
		t.Errorf("expected FixCI=true (default)")
	}
	if !captured.AutoReview {
		t.Errorf("expected AutoReview=true (default)")
	}
	if captured.MergeMethod != "rebase" {
		t.Errorf("expected MergeMethod=rebase (default), got %q", captured.MergeMethod)
	}
}

func TestRunConfigure_Asana(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader("2\n"), &out, allFoundChecker, ".", noopWriter)
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
	if !strings.Contains(output, "Workflow Configuration") {
		t.Errorf("expected workflow wizard section, got:\n%s", output)
	}
}

func TestRunConfigure_Asana_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// Input: tracker=2, project=1234, org=1(tags), tag=ready, section=(skip), completion=Done,
	// plan=n, fixci=y, autoreview=y, reviewer=, method=, containers=n, slack=n
	input := "2\n1234\n1\nready\n\nDone\nn\ny\ny\n\n\nn\nn\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured))
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
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader("3\n"), &out, allFoundChecker, ".", noopWriter)
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
	if !strings.Contains(output, "Workflow Configuration") {
		t.Errorf("expected workflow wizard section, got:\n%s", output)
	}
}

func TestRunConfigure_Linear_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// Input: tracker=3, team=team-xyz, org=1(labels), label=queued, completionState=Merged, then defaults
	input := "3\nteam-xyz\n1\n\nMerged\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured))
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

func TestRunConfigure_InvalidChoice(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader("5\n"), &out, allFoundChecker, ".", noopWriter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Invalid choice") {
		t.Errorf("expected invalid choice message, got:\n%s", output)
	}
}

func TestRunConfigure_EmptyInput(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, allFoundChecker, ".", noopWriter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Invalid choice") {
		t.Errorf("expected invalid choice message for empty input, got:\n%s", output)
	}
}

func TestRunConfigure_PrereqStatusSymbols(t *testing.T) {
	// git found (required), claude found (required), gh not found (optional)
	checker := partialChecker(map[string]bool{"git": true, "claude": true, "gh": false})
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader("\n"), &out, checker, ".", noopWriter)
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

func TestRunConfigure_RequiredMissingSymbol(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": true, "gh": true})
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(""), &out, checker, ".", noopWriter)
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
	err := runConfigureWithIO(strings.NewReader("1\n"), &out, allFoundChecker, ".", existsWriter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "already exists") {
		t.Errorf("expected 'already exists' message, got:\n%s", output)
	}
	// Should still show erg start since configure is otherwise complete
	if !strings.Contains(output, "erg start") {
		t.Errorf("expected erg start instruction even when file exists, got:\n%s", output)
	}
}

func TestRunConfigure_ShowsErgStart(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader("1\n"), &out, allFoundChecker, ".", noopWriter)
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
	// Input: tracker=2, project=1234, org=2(kanban), section="To do"(default), completion="Done"(default),
	// plan=n, fixci=y, autoreview=y, reviewer=, method=, containers=n, slack=n
	input := "2\n1234\n2\n\n\nn\ny\ny\n\n\nn\nn\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured))
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
	// Input: tracker=3, team=team-xyz, org=2(kanban), label=queued(default), completion="Done"(default),
	// plan=n, fixci=y, autoreview=y, reviewer=, method=, containers=n, slack=n
	input := "3\nteam-xyz\n2\n\n\nn\ny\ny\n\n\nn\nn\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured))
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

func TestRunConfigure_GitHub_Slack_WizardCaptures(t *testing.T) {
	var captured workflow.WizardConfig
	// Input: tracker=1, label=queued(default), plan=n, fixci=y, autoreview=y, reviewer=, method=, containers=n,
	// slack=y, webhook=$SLACK_WEBHOOK_URL
	input := "1\n\nn\ny\ny\n\n\nn\ny\n$SLACK_WEBHOOK_URL\n"
	var out bytes.Buffer
	err := runConfigureWithIO(strings.NewReader(input), &out, allFoundChecker, ".", captureWriter(&captured))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !captured.NotifySlack {
		t.Errorf("expected NotifySlack=true")
	}
	if captured.SlackWebhook != "$SLACK_WEBHOOK_URL" {
		t.Errorf("expected SlackWebhook=$SLACK_WEBHOOK_URL, got %q", captured.SlackWebhook)
	}
}

// alreadyExistsError simulates a "file already exists" error from WriteFromWizard.
type alreadyExistsError struct {
	fp string
}

func (e *alreadyExistsError) Error() string {
	return e.fp + " already exists"
}
