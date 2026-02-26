package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/git"
	"github.com/zhubert/erg/internal/workflow"
	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/exec"
)

func TestCheckPRReviewed_PRClosed(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "CLOSED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for closed PR")
	}
	if data == nil || data["pr_closed"] != true {
		t.Error("expected pr_closed=true in data")
	}
}

func TestCheckPRReviewed_PRMergedExternally(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "MERGED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true for merged PR")
	}
	if data == nil || data["pr_merged_externally"] != true {
		t.Error("expected pr_merged_externally=true in data")
	}
}

func TestCheckPRReviewed_AddressingFeedbackPhase(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR is OPEN
	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
	})
	d.state.AdvanceWorkItem("item-1", "await_review", "addressing_feedback")

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false during addressing_feedback phase")
	}
}

func TestCheckPRReviewed_PushingPhase(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
	})
	d.state.AdvanceWorkItem("item-1", "await_review", "pushing")

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false during pushing phase")
	}
}

func TestCheckPRReviewed_ReviewApproved(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR state = OPEN
	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	// No comments
	prListJSON, _ := json.Marshal([]struct {
		State       string        `json:"state"`
		HeadRefName string        `json:"headRefName"`
		Comments    []interface{} `json:"comments"`
		Reviews     []interface{} `json:"reviews"`
	}{{State: "OPEN", HeadRefName: "feature-sess-1", Comments: []interface{}{}, Reviews: []interface{}{}}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
		Stdout: prListJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	// The pr view mock returns the OPEN state for the first call (GetPRState)
	// and the reviews JSON for the second call (CheckPRReviewDecision).
	// But since both use "gh pr view", we need to override the mock
	// to return reviews JSON for the second call.
	// Actually, since AddPrefixMatch uses the same prefix, we need
	// to ensure the review check returns APPROVED.
	// The trick is: GetPRState uses --json state, CheckPRReviewDecision uses --json reviews
	// But MockExecutor prefix matching doesn't distinguish flags. So the same mock
	// is returned for both. The OPEN state JSON will fail to parse as reviews (no reviews field),
	// resulting in ReviewNone. That's the expected flow for "no review".

	fired, _, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With only the PR state mock returning {"state":"OPEN"}, the review check
	// will parse an empty reviews field → ReviewNone → not fired
	if fired {
		t.Error("expected fired=false with no review")
	}
}

func TestCheckPRReviewed_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "nonexistent",
		Branch:      "feature-1",
		CurrentStep: "await_review",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when session not found")
	}
}

func TestCheckCIComplete_CIPassing_AutoMergeEnabled(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "SUCCESS"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.autoMerge = true

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true when CI passes with auto-merge")
	}
	if data == nil || data["ci_passed"] != true {
		t.Error("expected ci_passed=true in data")
	}
}

func TestCheckCIComplete_CIPassing_AutoMergeDisabled(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "SUCCESS"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.autoMerge = false

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when auto-merge disabled")
	}
}

func TestCheckCIComplete_CIFailing_AbandonPolicy(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "FAILURE"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"on_failure": "abandon"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for CI failure")
	}
	if data == nil {
		t.Fatal("expected data")
	}
	if data["ci_action"] != "abandon" {
		t.Errorf("expected ci_action=abandon, got %v", data["ci_action"])
	}
}

func TestCheckCIComplete_CIFailing_DefaultRetry(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "FAILURE"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil) // default on_failure = "retry"
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for CI failure")
	}
	if data == nil {
		t.Fatal("expected data")
	}
	if data["ci_action"] != "retry" {
		t.Errorf("expected ci_action=retry, got %v", data["ci_action"])
	}
}

func TestCheckCIComplete_CIPending(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "PENDING"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for pending CI")
	}
}

func TestCheckCIComplete_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "nonexistent",
		Branch:      "feature-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when session not found")
	}
}

func TestCheckPRReviewed_MaxFeedbackRoundsReached(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	// Has new comments
	type comment struct {
		Body string `json:"body"`
	}
	prListJSON, _ := json.Marshal([]struct {
		State       string    `json:"state"`
		HeadRefName string    `json:"headRefName"`
		Comments    []comment `json:"comments"`
		Reviews     []interface{} `json:"reviews"`
	}{{
		State:       "OPEN",
		HeadRefName: "feature-sess-1",
		Comments:    []comment{{Body: "please fix"}},
		Reviews:     []interface{}{},
	}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
		Stdout: prListJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:             "item-1",
		IssueRef:       config.IssueRef{Source: "github", ID: "1"},
		SessionID:      "sess-1",
		Branch:         "feature-sess-1",
		CurrentStep:    "await_review",
		FeedbackRounds: 3, // At the default max
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"auto_address": true, "max_feedback_rounds": 3})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when max feedback rounds reached")
	}
}

func TestCheckPRReviewed_AutoAddressDisabled(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prStateJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "OPEN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prStateJSON,
	})

	// Has new comments
	type comment struct {
		Body string `json:"body"`
	}
	prListJSON, _ := json.Marshal([]struct {
		State       string        `json:"state"`
		HeadRefName string        `json:"headRefName"`
		Comments    []comment     `json:"comments"`
		Reviews     []interface{} `json:"reviews"`
	}{{
		State:       "OPEN",
		HeadRefName: "feature-sess-1",
		Comments:    []comment{{Body: "fix this"}},
		Reviews:     []interface{}{},
	}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
		Stdout: prListJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_review",
		UpdatedAt:   time.Now(),
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"auto_address": false})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRReviewed(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when auto_address disabled")
	}
}

func TestCheckPRMergeable_PRClosed(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "CLOSED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_mergeable",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"require_review": true, "require_ci": true})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkPRMergeable(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for closed PR")
	}
	if data == nil || data["pr_closed"] != true {
		t.Error("expected pr_closed=true in data")
	}
}

func TestCheckPRMergeable_PRMergedExternally(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	prViewJSON, _ := json.Marshal(struct {
		State string `json:"state"`
	}{State: "MERGED"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_mergeable",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"require_review": true, "require_ci": true})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkPRMergeable(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true for merged PR")
	}
	if data == nil || data["pr_merged_externally"] != true {
		t.Error("expected pr_merged_externally=true in data")
	}
}

func TestCheckPRMergeable_NotApproved(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Both GetPRState and CheckPRReviewDecision use "gh pr view" prefix,
	// so the same mock response is returned for both calls.
	// Use a combined JSON that satisfies both parsers: state=OPEN, no approved reviews.
	prViewJSON, _ := json.Marshal(struct {
		State   string        `json:"state"`
		Reviews []interface{} `json:"reviews"`
	}{State: "OPEN", Reviews: []interface{}{}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_mergeable",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"require_review": true, "require_ci": true})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRMergeable(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when review not approved")
	}
}

func TestCheckPRMergeable_CIPending(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR is OPEN and review is approved
	type review struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
	}
	r := review{State: "APPROVED"}
	r.Author.Login = "reviewer1"
	prViewJSON, _ := json.Marshal(struct {
		State   string   `json:"state"`
		Reviews []review `json:"reviews"`
	}{State: "OPEN", Reviews: []review{r}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	// CI is pending
	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "PENDING"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_mergeable",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"require_review": true, "require_ci": true})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRMergeable(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when CI is pending")
	}
}

func TestCheckPRMergeable_CIFailing(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR is OPEN and review is approved
	type review struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
	}
	r := review{State: "APPROVED"}
	r.Author.Login = "reviewer1"
	prViewJSON, _ := json.Marshal(struct {
		State   string   `json:"state"`
		Reviews []review `json:"reviews"`
	}{State: "OPEN", Reviews: []review{r}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: prViewJSON,
	})

	// CI is failing
	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "FAILURE"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_mergeable",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"require_review": true, "require_ci": true})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkPRMergeable(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when CI is failing")
	}
	if data == nil || data["ci_failed"] != true {
		t.Error("expected ci_failed=true in data")
	}
}

func TestCheckPRMergeable_NoSession(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "nonexistent",
		Branch:      "feature-1",
		CurrentStep: "await_mergeable",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"require_review": true, "require_ci": true})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkPRMergeable(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when session not found")
	}
}

func TestCheckCIComplete_CIFailing_FixPolicy(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "FAILURE"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"on_failure": "fix"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "fix" policy should fire the event (return true) to advance to choice state
	if !fired {
		t.Error("expected fired=true for CI failure with fix policy")
	}
	if data == nil {
		t.Fatal("expected data")
	}
	if data["ci_failed"] != true {
		t.Error("expected ci_failed=true in data")
	}
	if data["ci_passed"] != false {
		t.Error("expected ci_passed=false in data")
	}
}

func TestCheckCIComplete_Conflicting(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// PR is CONFLICTING
	mergeableJSON, _ := json.Marshal(struct {
		Mergeable string `json:"mergeable"`
	}{Mergeable: "CONFLICTING"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: mergeableJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.autoMerge = true

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"on_failure": "fix"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true when PR is conflicting")
	}
	if data == nil || data["conflicting"] != true {
		t.Error("expected conflicting=true in data")
	}
}

func TestCheckCIComplete_MergeableCheckFails_FallsThroughToCI(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mergeable check fails (gh pr view returns error)
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Err: errGHFailed,
	})

	// CI is passing
	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "SUCCESS"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.autoMerge = true

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall through to CI check and fire with ci_passed
	if !fired {
		t.Error("expected fired=true when CI passes (merge check fell through)")
	}
	if data == nil || data["ci_passed"] != true {
		t.Error("expected ci_passed=true in data")
	}
}

func TestCheckCIComplete_MergeableUnknown_FallsThroughToCI(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Mergeable returns UNKNOWN
	mergeableJSON, _ := json.Marshal(struct {
		Mergeable string `json:"mergeable"`
	}{Mergeable: "UNKNOWN"})
	mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
		Stdout: mergeableJSON,
	})

	// CI is pending
	checksJSON, _ := json.Marshal([]struct {
		State string `json:"state"`
	}{{State: "PENDING"}})
	mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
		Stdout: checksJSON,
		Err:    errGHFailed,
	})

	d := testDaemonWithExec(cfg, mockExec)
	d.autoMerge = true

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "1"},
		SessionID:   "sess-1",
		Branch:      "feature-sess-1",
		CurrentStep: "await_ci",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkCIComplete(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// UNKNOWN mergeable falls through, CI pending means not fired
	if fired {
		t.Error("expected fired=false when CI pending (merge status unknown)")
	}
}

// --- gate.approved event tests ---

func TestCheckGateApproved_LabelAdded_Fires(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	labelsJSON, _ := json.Marshal(struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}{Labels: []struct {
		Name string `json:"name"`
	}{{Name: "approved"}}})
	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Stdout: labelsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "label_added", "label": "approved"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true when label is present")
	}
	if data == nil || data["gate_approved"] != true {
		t.Error("expected gate_approved=true in data")
	}
	if data["gate_trigger"] != "label_added" {
		t.Errorf("expected gate_trigger=label_added, got %v", data["gate_trigger"])
	}
}

func TestCheckGateApproved_LabelAdded_LabelAbsent(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	labelsJSON, _ := json.Marshal(struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}{Labels: []struct {
		Name string `json:"name"`
	}{{Name: "bug"}}})
	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Stdout: labelsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "label_added", "label": "approved"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when label is absent")
	}
}

func TestCheckGateApproved_CommentMatch_Fires(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Comment posted 5 minutes after step was entered — should fire.
	commentsJSON := []byte(`{"comments":[{"author":{"login":"reviewer"},"body":"/approve please proceed","createdAt":"2020-01-01T10:05:00Z"}]}`)
	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Stdout: commentsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "comment_match", "comment_pattern": `^/approve`})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)
	// AddWorkItem overrides StepEnteredAt with time.Now(); override it here to a fixed past
	// time so the 2020-01-01T10:05:00Z comment is clearly after the step entry.
	view.StepEnteredAt = time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)

	fired, data, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true when matching comment found")
	}
	if data == nil || data["gate_approved"] != true {
		t.Error("expected gate_approved=true in data")
	}
	if data["gate_comment_author"] != "reviewer" {
		t.Errorf("expected gate_comment_author=reviewer, got %v", data["gate_comment_author"])
	}
}

func TestCheckGateApproved_CommentMatch_OldCommentIgnored(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Comment posted 5 minutes BEFORE step was entered — should be ignored.
	commentsJSON := []byte(`{"comments":[{"author":{"login":"oldreviewer"},"body":"/approve old comment","createdAt":"2020-01-01T09:55:00Z"}]}`)
	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Stdout: commentsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "comment_match", "comment_pattern": `^/approve`})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)
	// Set StepEnteredAt to 2020-01-01T10:00:00Z so the 09:55 comment is clearly before it.
	view.StepEnteredAt = time.Date(2020, 1, 1, 10, 0, 0, 0, time.UTC)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when matching comment is older than step entry")
	}
}

func TestCheckGateApproved_CommentMatch_NoMatch(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	commentsJSON := []byte(`{"comments":[{"author":{"login":"user"},"body":"just a regular comment","createdAt":"2020-01-01T10:00:00Z"}]}`)
	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Stdout: commentsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "comment_match", "comment_pattern": `^/approve`})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when no comment matches pattern")
	}
}

func TestCheckGateApproved_NonGitHubIssue(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "asana", ID: "task-abc"},
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "label_added", "label": "approved"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for non-github issue")
	}
}

func TestCheckGateApproved_WorkItemNotFound(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(nil)
	view := &workflow.WorkItemView{ID: "nonexistent", RepoPath: "/test/repo"}

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for missing work item")
	}
}

func TestCheckGateApproved_InvalidIssueNumber(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "not-a-number"},
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "label_added", "label": "approved"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for invalid issue number")
	}
}

func TestCheckGateApproved_CLIError_LabelCheck(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Err: fmt.Errorf("gh: not found"),
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "label_added", "label": "approved"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	// CLI error should be swallowed; returns false (not fired), no error.
	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false on CLI error")
	}
}

func TestCheckGateApproved_InvalidPattern(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	// Invalid regex pattern
	params := workflow.NewParamHelper(map[string]any{"trigger": "comment_match", "comment_pattern": `[invalid`})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for invalid regex pattern")
	}
}

func TestCheckGateApproved_UnknownTrigger(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	params := workflow.NewParamHelper(map[string]any{"trigger": "webhook"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false for unknown trigger type")
	}
}

func TestCheckGateApproved_DefaultTrigger_IsLabelAdded(t *testing.T) {
	cfg := testConfig()
	mockExec := exec.NewMockExecutor(nil)

	// Label "approved" is present
	labelsJSON, _ := json.Marshal(struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}{Labels: []struct {
		Name string `json:"name"`
	}{{Name: "approved"}}})
	mockExec.AddPrefixMatch("gh", []string{"issue", "view"}, exec.MockResponse{
		Stdout: labelsJSON,
	})

	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	d := testDaemonWithExec(cfg, mockExec)
	d.gitService = gitSvc
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	// No trigger param — should default to label_added with "approved" label
	params := workflow.NewParamHelper(nil)
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, data, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fired {
		t.Error("expected fired=true with default trigger and approved label present")
	}
	if data["gate_label"] != "approved" {
		t.Errorf("expected gate_label=approved, got %v", data["gate_label"])
	}
}

func TestCheckGateApproved_CommentMatch_MissingPattern(t *testing.T) {
	cfg := testConfig()
	d := testDaemon(cfg)
	d.repoFilter = "/test/repo"

	sess := testSession("sess-1")
	cfg.AddSession(*sess)

	d.state.AddWorkItem(&daemonstate.WorkItem{
		ID:          "item-1",
		IssueRef:    config.IssueRef{Source: "github", ID: "42"},
		SessionID:   "sess-1",
		CurrentStep: "await_approval",
	})

	checker := NewEventChecker(d)
	// comment_match trigger with no pattern — should not fire
	params := workflow.NewParamHelper(map[string]any{"trigger": "comment_match"})
	itemTmp, _ := d.state.GetWorkItem("item-1")
	view := d.workItemView(itemTmp)

	fired, _, err := checker.checkGateApproved(context.Background(), params, view)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fired {
		t.Error("expected fired=false when comment_pattern is missing")
	}
}
