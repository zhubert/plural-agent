package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/zhubert/plural-agent/internal/agentconfig"
	"github.com/zhubert/plural-core/claude"
	"github.com/zhubert/plural-core/config"
	"github.com/zhubert/plural-core/exec"
	"github.com/zhubert/plural-core/git"
	"github.com/zhubert/plural-core/manager"
)

// mockHost implements worker.Host for unit testing auto-merge functions.
type mockHost struct {
	cfg        *config.Config
	gitService *git.GitService
	sessionMgr *manager.SessionManager
	logger     *slog.Logger

	maxTurns              int
	maxDuration           int
	autoMerge             bool
	mergeMethod           string
	daemonManaged         bool
	autoAddressPRComments bool

	cleanupCalled    map[string]bool
	autoCreatePRCalled map[string]bool
}

func newMockHost(mockExec *exec.MockExecutor) *mockHost {
	cfg := &config.Config{
		AutoMaxTurns:       50,
		AutoMaxDurationMin: 30,
	}
	gitSvc := git.NewGitServiceWithExecutor(mockExec)
	sessMgr := manager.NewSessionManager(cfg, gitSvc)
	sessMgr.SetSkipMessageLoad(true)

	return &mockHost{
		cfg:           cfg,
		gitService:    gitSvc,
		sessionMgr:    sessMgr,
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		maxTurns:      50,
		maxDuration:   30,
		autoMerge:     true,
		mergeMethod:   "rebase",
		cleanupCalled:      make(map[string]bool),
		autoCreatePRCalled: make(map[string]bool),
	}
}

// Compile-time check that mockHost implements Host.
var _ Host = (*mockHost)(nil)

func (h *mockHost) Config() agentconfig.Config                { return h.cfg }
func (h *mockHost) GitService() *git.GitService               { return h.gitService }
func (h *mockHost) SessionManager() *manager.SessionManager    { return h.sessionMgr }
func (h *mockHost) Logger() *slog.Logger                      { return h.logger }
func (h *mockHost) MaxTurns() int                             { return h.maxTurns }
func (h *mockHost) MaxDuration() int                          { return h.maxDuration }
func (h *mockHost) AutoMerge() bool                           { return h.autoMerge }
func (h *mockHost) MergeMethod() string                       { return h.mergeMethod }
func (h *mockHost) DaemonManaged() bool                       { return h.daemonManaged }
func (h *mockHost) AutoAddressPRComments() bool               { return h.autoAddressPRComments }

func (h *mockHost) AutoCreatePR(ctx context.Context, sessionID string) (string, error) {
	h.autoCreatePRCalled[sessionID] = true
	return "https://github.com/test/repo/pull/1", nil
}

func (h *mockHost) CreateChildSession(ctx context.Context, supervisorID, taskDescription string) (SessionInfo, error) {
	return SessionInfo{}, nil
}

func (h *mockHost) CleanupSession(ctx context.Context, sessionID string) error {
	h.cleanupCalled[sessionID] = true
	return nil
}

func (h *mockHost) SaveRunnerMessages(sessionID string, runner claude.RunnerInterface) {}

func (h *mockHost) IsWorkerRunning(sessionID string) bool { return false }

// reviewsJSON wraps reviews in the expected {"reviews": [...]} format.
func reviewsJSON(reviews ...struct {
	State  string `json:"state"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}) []byte {
	wrapper := struct {
		Reviews interface{} `json:"reviews"`
	}{Reviews: reviews}
	data, _ := json.Marshal(wrapper)
	return data
}

func TestCheckReviewApproval(t *testing.T) {
	t.Run("approved", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		type review struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		}
		r := review{State: "APPROVED"}
		r.Author.Login = "reviewer"
		data := reviewsJSON(r)
		mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
			Stdout: data,
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckReviewApproval(context.Background(), h, "s1", sess, 1)
		if action != MergeActionProceed {
			t.Errorf("expected MergeActionProceed, got %d", action)
		}
	})

	t.Run("changes requested", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		type review struct {
			State  string `json:"state"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		}
		r := review{State: "CHANGES_REQUESTED"}
		r.Author.Login = "reviewer"
		data := reviewsJSON(r)
		mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
			Stdout: data,
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckReviewApproval(context.Background(), h, "s1", sess, 1)
		if action != MergeActionContinue {
			t.Errorf("expected MergeActionContinue, got %d", action)
		}
	})

	t.Run("no review yet", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		// Empty reviews
		data, _ := json.Marshal(struct {
			Reviews []interface{} `json:"reviews"`
		}{Reviews: []interface{}{}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
			Stdout: data,
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckReviewApproval(context.Background(), h, "s1", sess, 1)
		if action != MergeActionContinue {
			t.Errorf("expected MergeActionContinue, got %d", action)
		}
	})

	t.Run("no review at max attempts", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		data, _ := json.Marshal(struct {
			Reviews []interface{} `json:"reviews"`
		}{Reviews: []interface{}{}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "view"}, exec.MockResponse{
			Stdout: data,
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckReviewApproval(context.Background(), h, "s1", sess, maxAutoMergePollAttempts)
		if action != MergeActionStop {
			t.Errorf("expected MergeActionStop at max attempts, got %d", action)
		}
	})
}

func TestCheckCIAndMerge(t *testing.T) {
	t.Run("CI passing merges", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		// CI passing
		checksJSON, _ := json.Marshal([]struct {
			State string `json:"state"`
		}{{State: "SUCCESS"}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
			Stdout: checksJSON,
		})

		// Merge succeeds
		mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
			Stdout: []byte("merged"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckCIAndMerge(context.Background(), h, "s1", sess, 1)
		if action != MergeActionProceed {
			t.Errorf("expected MergeActionProceed, got %d", action)
		}
	})

	t.Run("CI failing stops", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		// gh pr checks returns non-zero exit code for failure, with JSON in stdout
		checksJSON, _ := json.Marshal([]struct {
			State string `json:"state"`
		}{{State: "FAILURE"}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
			Stdout: checksJSON,
			Err:    fmt.Errorf("checks failed"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckCIAndMerge(context.Background(), h, "s1", sess, 1)
		if action != MergeActionStop {
			t.Errorf("expected MergeActionStop, got %d", action)
		}
	})

	t.Run("CI pending continues", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		// gh pr checks returns non-zero exit code for pending, with JSON in stdout
		checksJSON, _ := json.Marshal([]struct {
			State string `json:"state"`
		}{{State: "PENDING"}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
			Stdout: checksJSON,
			Err:    fmt.Errorf("checks pending"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckCIAndMerge(context.Background(), h, "s1", sess, 1)
		if action != MergeActionContinue {
			t.Errorf("expected MergeActionContinue, got %d", action)
		}
	})

	t.Run("CI pending at max attempts stops", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		checksJSON, _ := json.Marshal([]struct {
			State string `json:"state"`
		}{{State: "PENDING"}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "checks"}, exec.MockResponse{
			Stdout: checksJSON,
			Err:    fmt.Errorf("checks pending"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckCIAndMerge(context.Background(), h, "s1", sess, maxAutoMergePollAttempts)
		if action != MergeActionStop {
			t.Errorf("expected MergeActionStop at max attempts, got %d", action)
		}
	})
}

func TestCheckAndAddressComments(t *testing.T) {
	t.Run("no new comments proceeds", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		// No comments
		prListJSON, _ := json.Marshal([]struct {
			State       string        `json:"state"`
			HeadRefName string        `json:"headRefName"`
			Comments    []interface{} `json:"comments"`
			Reviews     []interface{} `json:"reviews"`
		}{{State: "OPEN", HeadRefName: "feat-1", Comments: []interface{}{}, Reviews: []interface{}{}}})
		mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
			Stdout: prListJSON,
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckAndAddressComments(context.Background(), h, "s1", sess, 1)
		if action != MergeActionProceed {
			t.Errorf("expected MergeActionProceed, got %d", action)
		}
	})

	t.Run("comment check error gracefully proceeds", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		mockExec.AddPrefixMatch("gh", []string{"pr", "list"}, exec.MockResponse{
			Err: fmt.Errorf("gh: command failed"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := CheckAndAddressComments(context.Background(), h, "s1", sess, 1)
		if action != MergeActionProceed {
			t.Errorf("expected MergeActionProceed on error, got %d", action)
		}
	})
}

func TestDoMerge(t *testing.T) {
	t.Run("merge success", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
			Stdout: []byte("merged"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := DoMerge(context.Background(), h, "s1", sess)
		if action != MergeActionProceed {
			t.Errorf("expected MergeActionProceed, got %d", action)
		}

		// Verify session marked as merged
		updated := h.cfg.GetSession("s1")
		if updated != nil && !updated.PRMerged {
			t.Error("expected session to be marked as merged")
		}
	})

	t.Run("merge failure stops", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
			Err: fmt.Errorf("merge conflict"),
		})

		h := newMockHost(mockExec)
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		action := DoMerge(context.Background(), h, "s1", sess)
		if action != MergeActionStop {
			t.Errorf("expected MergeActionStop, got %d", action)
		}
	})

	t.Run("merge success triggers cleanup when enabled", func(t *testing.T) {
		mockExec := exec.NewMockExecutor(nil)

		mockExec.AddPrefixMatch("gh", []string{"pr", "merge"}, exec.MockResponse{
			Stdout: []byte("merged"),
		})

		h := newMockHost(mockExec)
		h.cfg.AutoCleanupMerged = true
		sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
		h.cfg.AddSession(*sess)

		DoMerge(context.Background(), h, "s1", sess)

		if !h.cleanupCalled["s1"] {
			t.Error("expected CleanupSession to be called")
		}
	})
}

func TestRunAutoMerge_ContextCancellation(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)
	sess := &config.Session{ID: "s1", RepoPath: "/repo", Branch: "feat-1"}
	h.cfg.AddSession(*sess)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		RunAutoMerge(ctx, h, "s1")
		close(done)
	}()

	// Cancel immediately
	cancel()

	select {
	case <-done:
		// Good — returned promptly
	case <-time.After(2 * time.Second):
		t.Fatal("RunAutoMerge did not return promptly after context cancellation")
	}
}

func TestRunAutoMerge_NilSession(t *testing.T) {
	mockExec := exec.NewMockExecutor(nil)
	h := newMockHost(mockExec)

	// No session added — should return immediately
	done := make(chan struct{})
	go func() {
		RunAutoMerge(context.Background(), h, "nonexistent")
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(1 * time.Second):
		t.Fatal("RunAutoMerge did not return for nil session")
	}
}
