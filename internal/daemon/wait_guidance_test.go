package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/issues"
	"github.com/zhubert/erg/internal/workflow"
)

// guidanceTestProvider is a minimal provider that captures Comment calls.
type guidanceTestProvider struct {
	src      issues.Source
	comments []string
}

func (p *guidanceTestProvider) Name() string                             { return string(p.src) }
func (p *guidanceTestProvider) Source() issues.Source                    { return p.src }
func (p *guidanceTestProvider) IsConfigured(_ string) bool               { return true }
func (p *guidanceTestProvider) GenerateBranchName(_ issues.Issue) string { return "" }
func (p *guidanceTestProvider) GetPRLinkText(_ issues.Issue) string      { return "" }
func (p *guidanceTestProvider) FetchIssues(_ context.Context, _ string, _ issues.FilterConfig) ([]issues.Issue, error) {
	return nil, nil
}
func (p *guidanceTestProvider) RemoveLabel(_ context.Context, _ string, _ string, _ string) error {
	return nil
}
func (p *guidanceTestProvider) Comment(_ context.Context, _, _, body string) error {
	p.comments = append(p.comments, body)
	return nil
}

func makeGuidanceItem(source, issueID string) daemonstate.WorkItem {
	return daemonstate.WorkItem{
		ID: "wi-guidance-test",
		IssueRef: config.IssueRef{
			Source: source,
			ID:     issueID,
			Title:  "Test issue",
		},
	}
}

func TestPostWaitGuidance_SkipsAutomatedEvents(t *testing.T) {
	automated := []string{"ci.complete", "ci.wait_for_checks", "pr.mergeable"}
	for _, event := range automated {
		t.Run(event, func(t *testing.T) {
			cfg := testConfig()
			d := testDaemon(cfg)
			prov := &guidanceTestProvider{src: issues.SourceGitHub}
			d.issueRegistry = issues.NewProviderRegistry(prov)

			item := makeGuidanceItem("github", "42")
			state := &workflow.State{
				Type:  workflow.StateTypeWait,
				Event: event,
			}
			d.postWaitGuidance(context.Background(), item, "wait_step", state)

			if len(prov.comments) != 0 {
				t.Errorf("expected no comment for automated event %q, got %d", event, len(prov.comments))
			}
		})
	}
}

func TestPostWaitGuidance_PostsForGateApproved(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	prov := &guidanceTestProvider{src: issues.SourceGitHub}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	// Add a session so commentOnIssue can resolve the repo path
	sess := &config.Session{
		ID:       "sess-gate",
		RepoPath: "/test/repo",
		Branch:   "feat-1",
		IssueRef: &config.IssueRef{Source: "github", ID: "42"},
	}
	cfg.AddSession(*sess)

	item := makeGuidanceItem("github", "42")
	item.SessionID = "sess-gate"

	state := &workflow.State{
		Type:  workflow.StateTypeWait,
		Event: "gate.approved",
		Params: map[string]any{
			"trigger": "label_added",
			"label":   "approved",
		},
	}
	d.postWaitGuidance(context.Background(), item, "await_gate", state)

	if len(prov.comments) == 0 {
		t.Fatal("expected a comment to be posted")
	}
	body := prov.comments[0]
	if !strings.Contains(body, "approved") {
		t.Errorf("expected label name in comment, got: %q", body)
	}
}

func TestPostWaitGuidance_PostsForPlanUserReplied(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	prov := &guidanceTestProvider{src: issues.SourceGitHub}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	sess := &config.Session{
		ID:       "sess-plan",
		RepoPath: "/test/repo",
		Branch:   "feat-plan",
		IssueRef: &config.IssueRef{Source: "github", ID: "10"},
	}
	cfg.AddSession(*sess)

	item := makeGuidanceItem("github", "10")
	item.SessionID = "sess-plan"

	state := &workflow.State{
		Type:  workflow.StateTypeWait,
		Event: "plan.user_replied",
		Params: map[string]any{
			"approval_pattern": `(?i)(LGTM|looks good|approved?)`,
		},
	}
	d.postWaitGuidance(context.Background(), item, "await_plan_feedback", state)

	if len(prov.comments) == 0 {
		t.Fatal("expected a comment to be posted")
	}
	body := prov.comments[0]
	if !strings.Contains(body, "plan") {
		t.Errorf("expected 'plan' in comment body, got: %q", body)
	}
}

func TestPostWaitGuidance_PRReviewed_IncludesPRURL(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	prov := &guidanceTestProvider{src: issues.SourceGitHub}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	sess := &config.Session{
		ID:       "sess-pr",
		RepoPath: "/test/repo",
		Branch:   "feat-pr",
		IssueRef: &config.IssueRef{Source: "github", ID: "7"},
	}
	cfg.AddSession(*sess)

	prURL := "https://github.com/owner/repo/pull/99"
	item := makeGuidanceItem("github", "7")
	item.SessionID = "sess-pr"
	item.PRURL = prURL

	state := &workflow.State{
		Type:  workflow.StateTypeWait,
		Event: "pr.reviewed",
	}
	d.postWaitGuidance(context.Background(), item, "await_review", state)

	if len(prov.comments) == 0 {
		t.Fatal("expected a comment to be posted")
	}
	body := prov.comments[0]
	if !strings.Contains(body, prURL) {
		t.Errorf("expected PR URL in comment, got: %q", body)
	}
}

func TestPostWaitGuidance_ExplicitSuppression(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	prov := &guidanceTestProvider{src: issues.SourceGitHub}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	sess := &config.Session{
		ID:       "sess-suppress",
		RepoPath: "/test/repo",
		Branch:   "feat-suppress",
		IssueRef: &config.IssueRef{Source: "github", ID: "5"},
	}
	cfg.AddSession(*sess)

	item := makeGuidanceItem("github", "5")
	item.SessionID = "sess-suppress"

	empty := ""
	state := &workflow.State{
		Type:     workflow.StateTypeWait,
		Event:    "gate.approved",
		Guidance: &empty, // explicit suppression
	}
	d.postWaitGuidance(context.Background(), item, "await_gate", state)

	if len(prov.comments) != 0 {
		t.Errorf("expected no comment when guidance is suppressed, got %d", len(prov.comments))
	}
}

func TestPostWaitGuidance_CustomGuidanceMessage(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	prov := &guidanceTestProvider{src: issues.SourceGitHub}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	sess := &config.Session{
		ID:       "sess-custom",
		RepoPath: "/test/repo",
		Branch:   "feat-custom",
		IssueRef: &config.IssueRef{Source: "github", ID: "3"},
	}
	cfg.AddSession(*sess)

	item := makeGuidanceItem("github", "3")
	item.SessionID = "sess-custom"

	custom := "Do the special approval dance."
	state := &workflow.State{
		Type:     workflow.StateTypeWait,
		Event:    "gate.approved",
		Guidance: &custom,
	}
	d.postWaitGuidance(context.Background(), item, "await_gate", state)

	if len(prov.comments) == 0 {
		t.Fatal("expected a comment to be posted")
	}
	body := prov.comments[0]
	if !strings.Contains(body, custom) {
		t.Errorf("expected custom guidance in comment, got: %q", body)
	}
}

func TestPostWaitGuidance_AsanaProvider(t *testing.T) {
	cfg := testConfig()
	cfg.AddRepo("/test/repo")
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"
	prov := &guidanceTestProvider{src: issues.SourceAsana}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	item := makeGuidanceItem("asana", "task-123")
	state := &workflow.State{
		Type:   workflow.StateTypeWait,
		Event:  "asana.in_section",
		Params: map[string]any{"section": "Doing"},
	}
	d.postWaitGuidance(context.Background(), item, "await_doing", state)

	if len(prov.comments) == 0 {
		t.Fatal("expected a comment to be posted via Asana provider")
	}
	body := prov.comments[0]
	if !strings.Contains(body, "Doing") {
		t.Errorf("expected section name in comment, got: %q", body)
	}
}

func TestPostWaitGuidance_LinearProvider(t *testing.T) {
	cfg := testConfig()
	cfg.AddRepo("/test/repo")
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"
	prov := &guidanceTestProvider{src: issues.SourceLinear}
	d.issueRegistry = issues.NewProviderRegistry(prov)

	item := makeGuidanceItem("linear", "issue-abc")
	state := &workflow.State{
		Type:   workflow.StateTypeWait,
		Event:  "linear.in_state",
		Params: map[string]any{"state": "In Progress"},
	}
	d.postWaitGuidance(context.Background(), item, "await_doing", state)

	if len(prov.comments) == 0 {
		t.Fatal("expected a comment to be posted via Linear provider")
	}
	body := prov.comments[0]
	if !strings.Contains(body, "In Progress") {
		t.Errorf("expected state name in comment, got: %q", body)
	}
}

func TestPostWaitGuidance_UnknownSource_NoComment(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	item := makeGuidanceItem("jira", "PROJ-42")
	state := &workflow.State{
		Type:  workflow.StateTypeWait,
		Event: "gate.approved",
	}
	// Should not panic — just log and return
	d.postWaitGuidance(context.Background(), item, "await_gate", state)
}
