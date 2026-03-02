package cmd

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/cli"
	"github.com/zhubert/erg/internal/workflow"
)

// slowReader wraps an io.Reader and reads one byte at a time.
// This prevents bufio.Scanner (used by huh's accessible mode) from buffering
// ahead, which would consume input meant for subsequent form fields.
type slowReader struct {
	r io.Reader
}

func (sr *slowReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return sr.r.Read(p[:1])
}

func newTestInput(s string) io.Reader {
	return &slowReader{r: strings.NewReader(s)}
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

// githubDefaultInput provides enough input to complete a GitHub configure flow
// with all defaults: tracker=GitHub, label=queued, all behavior defaults, confirm=y.
//
// Fields consumed (in order):
//  1. Select tracker: "1" → github
//  2. Note (setup): no input
//  3. Input label: "" → keeps "queued"
//  4. Confirm plan: "" → keeps false (N)
//  5. Confirm fixCI: "" → keeps true (y)
//  6. Confirm autoReview: "" → keeps true (y)
//  7. Input reviewer: "" → keeps ""
//  8. Select merge: "" → keeps rebase (1)
//  9. Confirm containers: "" → keeps false (N)
//
// 10. Confirm slack: "" → keeps false (N)
// 11. Note (summary): no input
// 12. Confirm write: "y"
const githubDefaultInput = "1\n\n\n\n\n\n\n\n\ny\n"

func TestRunConfigure_AllPrereqsMissing(t *testing.T) {
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(""), &out, noneFoundChecker, ".", noopWriter, true)
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
	if strings.Contains(output, "issue tracker") {
		t.Errorf("should not show issue tracker prompt when prereqs are missing, got:\n%s", output)
	}
}

func TestRunConfigure_RequiredMissing_ShowsInstallURLs(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": false, "gh": true})
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(""), &out, checker, ".", noopWriter, true)
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
	err := runConfigureWithIO(newTestInput(githubDefaultInput), &out, allFoundChecker, ".", noopWriter, true)
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
	// tracker=1(GitHub), label=myqueue, plan=y, fixci=n, autoreview=n, reviewer=alice,
	// method=2(squash), containers=y, slack=n, confirm=y
	input := "1\nmyqueue\ny\nn\nn\nalice\n2\ny\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
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
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(githubDefaultInput), &out, allFoundChecker, ".", captureWriter(&captured), true)
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
	// tracker=2(Asana), project=proj123, org=1(tags), tag=queued(default),
	// section=(skip), completion=(skip), plan=N, fixci=Y, autoreview=Y,
	// reviewer=(skip), merge=1(rebase), containers=N, slack=N, confirm=y
	input := "2\nproj123\n1\n\n\n\n\n\n\n\n1\n\n\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", noopWriter, true)
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
	// tracker=2(Asana), project=1234, org=1(tags), tag=ready,
	// section=(skip), completion=Done, plan=n, fixci=y, autoreview=y,
	// reviewer=(skip), merge=1(rebase), containers=n, slack=n, confirm=y
	input := "2\n1234\n1\nready\n\nDone\nn\ny\ny\n\n1\nn\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
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
	// tracker=3(Linear), team=team1, org=1(labels), label=queued(default),
	// completionState=(skip), plan=N, fixci=Y, autoreview=Y,
	// reviewer=(skip), merge=1(rebase), containers=N, slack=N, confirm=y
	input := "3\nteam1\n1\n\n\n\n\n\n\n1\n\n\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", noopWriter, true)
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
	// tracker=3(Linear), team=team-xyz, org=1(labels), label=queued(default),
	// completionState=Merged, plan=N, fixci=Y, autoreview=Y,
	// reviewer=(skip), merge=1(rebase), containers=N, slack=N, confirm=y
	input := "3\nteam-xyz\n1\n\nMerged\n\n\n\n\n1\n\n\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
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
	// git found (required), claude found (required), gh not found (optional)
	checker := partialChecker(map[string]bool{"git": true, "claude": true, "gh": false})
	var out bytes.Buffer
	// Prereqs pass, so provide full input to complete the flow
	err := runConfigureWithIO(newTestInput(githubDefaultInput), &out, checker, ".", noopWriter, true)
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
	// All required tools present, so should proceed
	if !strings.Contains(output, "All prerequisites installed") {
		t.Errorf("expected success message when required tools present, got:\n%s", output)
	}
}

func TestRunConfigure_RequiredMissingSymbol(t *testing.T) {
	checker := partialChecker(map[string]bool{"git": false, "claude": true, "gh": true})
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(""), &out, checker, ".", noopWriter, true)
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
	err := runConfigureWithIO(newTestInput(githubDefaultInput), &out, allFoundChecker, ".", existsWriter, true)
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
	err := runConfigureWithIO(newTestInput(githubDefaultInput), &out, allFoundChecker, ".", noopWriter, true)
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
	// tracker=2(Asana), project=1234, org=2(kanban), section=To do(default),
	// completion=Done(default), plan=n, fixci=Y(default), autoreview=Y(default),
	// reviewer=(skip), merge=1(rebase), containers=n, slack=n, confirm=y
	input := "2\n1234\n2\n\n\nn\n\n\n\n1\nn\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
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
	// tracker=3(Linear), team=team-xyz, org=2(kanban), label=queued(default),
	// completionState=Done(default), plan=n, fixci=Y(default), autoreview=Y(default),
	// reviewer=(skip), merge=1(rebase), containers=n, slack=n, confirm=y
	input := "3\nteam-xyz\n2\n\n\nn\n\n\n\n1\nn\nn\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
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
	// tracker=1(GitHub), label=queued(default), plan=n, fixci=y, autoreview=y,
	// reviewer=(skip), merge=1(rebase), containers=n, slack=y, webhook=$SLACK_WEBHOOK_URL,
	// confirm=y
	input := "1\n\nn\ny\ny\n\n1\nn\ny\n$SLACK_WEBHOOK_URL\ny\n"
	var out bytes.Buffer
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", captureWriter(&captured), true)
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

func TestRunConfigure_ConfirmCancelled(t *testing.T) {
	// tracker=1(GitHub), label=queued(default), all behavior defaults, confirm=n
	input := "1\n\n\n\n\n\n\n\n\nn\n"
	var out bytes.Buffer
	writerCalled := false
	neverWriter := func(repoPath string, cfg workflow.WizardConfig) (string, error) {
		writerCalled = true
		return "", nil
	}
	err := runConfigureWithIO(newTestInput(input), &out, allFoundChecker, ".", neverWriter, true)
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
