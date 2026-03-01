package issues

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/zhubert/erg/internal/config"
)

func TestAsanaProvider_Name(t *testing.T) {
	p := NewAsanaProvider(nil)
	if p.Name() != "Asana Tasks" {
		t.Errorf("expected 'Asana Tasks', got '%s'", p.Name())
	}
}

func TestAsanaProvider_Source(t *testing.T) {
	p := NewAsanaProvider(nil)
	if p.Source() != SourceAsana {
		t.Errorf("expected SourceAsana, got '%s'", p.Source())
	}
}

func TestAsanaProvider_IsConfigured(t *testing.T) {
	// Create a temporary config
	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "12345")

	p := NewAsanaProvider(cfg)

	// Save and restore env var
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)

	// Test without PAT
	os.Setenv(asanaPATEnvVar, "")
	if p.IsConfigured("/test/repo") {
		t.Error("expected IsConfigured=false without PAT")
	}

	// Test with PAT but without project mapping
	os.Setenv(asanaPATEnvVar, "test-pat")
	if p.IsConfigured("/other/repo") {
		t.Error("expected IsConfigured=false without project mapping")
	}

	// Test with both PAT and project mapping
	if !p.IsConfigured("/test/repo") {
		t.Error("expected IsConfigured=true with PAT and project mapping")
	}
}

func TestAsanaProvider_GenerateBranchName(t *testing.T) {
	p := NewAsanaProvider(nil)

	tests := []struct {
		name     string
		issue    Issue
		expected string
	}{
		{"simple title", Issue{ID: "123", Title: "Fix login bug"}, "task-fix-login-bug"},
		{"uppercase", Issue{ID: "123", Title: "URGENT Fix"}, "task-urgent-fix"},
		{"special chars", Issue{ID: "123", Title: "Fix bug #42"}, "task-fix-bug-42"},
		{"long title", Issue{ID: "123", Title: "This is a very long task title that should be truncated to keep branch names reasonable"}, "task-this-is-a-very-long-task-title-that-shou"},
		{"only special chars", Issue{ID: "123", Title: "!@#$%"}, "task-123"},
		{"empty title", Issue{ID: "123", Title: ""}, "task-123"},
		{"trailing hyphen", Issue{ID: "123", Title: "Fix bug - "}, "task-fix-bug"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.GenerateBranchName(tc.issue)
			if result != tc.expected {
				t.Errorf("GenerateBranchName(%q) = %s, expected %s", tc.issue.Title, result, tc.expected)
			}
		})
	}
}

func TestAsanaProvider_GetPRLinkText(t *testing.T) {
	p := NewAsanaProvider(nil)

	// Asana doesn't support auto-close
	result := p.GetPRLinkText(Issue{ID: "123", Source: SourceAsana})
	if result != "" {
		t.Errorf("expected empty string, got '%s'", result)
	}
}

func TestAsanaProvider_FetchIssues_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)

	os.Setenv(asanaPATEnvVar, "")

	cfg := &config.Config{}
	p := NewAsanaProvider(cfg)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Project: "12345"})
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_FetchIssues_NoProjectID(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)

	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProvider(cfg)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{})
	if err == nil {
		t.Error("expected error without project ID")
	}
}

func TestAsanaProvider_FetchIssues_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-pat" {
			t.Errorf("expected 'Bearer test-pat', got '%s'", auth)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		response := asanaTasksResponse{
			Data: []asanaTask{
				{GID: "1234567890", Name: "Task 1", Notes: "Description 1", Permalink: "https://app.asana.com/0/123/1234567890"},
				{GID: "0987654321", Name: "Task 2", Notes: "Description 2", Permalink: "https://app.asana.com/0/123/0987654321"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Project: "12345"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Title != "Task 1" {
		t.Errorf("expected title 'Task 1', got %q", issues[0].Title)
	}
	if issues[0].Source != SourceAsana {
		t.Errorf("expected source SourceAsana, got %q", issues[0].Source)
	}
}

func TestAsanaProvider_FetchIssues_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Project: "12345"})
	if err == nil {
		t.Error("expected error from API error response")
	}
}

func TestAsanaProvider_FetchIssues_BySection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/projects/12345/sections"):
			json.NewEncoder(w).Encode(asanaSectionsResponse{
				Data: []asanaSection{
					{GID: "sec-todo", Name: "Todo"},
					{GID: "sec-doing", Name: "Doing"},
				},
			})
		case strings.Contains(r.URL.Path, "/sections/sec-todo/tasks"):
			json.NewEncoder(w).Encode(asanaTasksResponse{
				Data: []asanaTask{
					{GID: "task-1", Name: "Todo Task 1", Notes: "desc1", Permalink: "https://app.asana.com/1"},
					{GID: "task-2", Name: "Todo Task 2", Notes: "desc2", Permalink: "https://app.asana.com/2"},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	issues, err := p.FetchIssues(context.Background(), "/test/repo", FilterConfig{Project: "12345", Section: "Todo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].ID != "task-1" {
		t.Errorf("expected task-1, got %s", issues[0].ID)
	}
}

func TestAsanaProvider_FetchIssues_BySectionCaseInsensitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/projects/12345/sections"):
			json.NewEncoder(w).Encode(asanaSectionsResponse{
				Data: []asanaSection{
					{GID: "sec-todo", Name: "TODO"},
				},
			})
		case strings.Contains(r.URL.Path, "/sections/sec-todo/tasks"):
			json.NewEncoder(w).Encode(asanaTasksResponse{
				Data: []asanaTask{
					{GID: "task-1", Name: "Task 1", Notes: "n", Permalink: "u"},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	// Use lowercase "todo" to match "TODO" section — should match case-insensitively.
	issues, err := p.FetchIssues(context.Background(), "/repo", FilterConfig{Project: "12345", Section: "todo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
}

func TestAsanaProvider_FetchIssues_SectionNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(asanaSectionsResponse{
			Data: []asanaSection{
				{GID: "sec-doing", Name: "Doing"},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	_, err := p.FetchIssues(context.Background(), "/repo", FilterConfig{Project: "12345", Section: "Todo"})
	if err == nil {
		t.Error("expected error when section is not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestAsanaProvider_FetchIssues_SectionWithLabelFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/projects/12345/sections"):
			json.NewEncoder(w).Encode(asanaSectionsResponse{
				Data: []asanaSection{
					{GID: "sec-todo", Name: "Todo"},
				},
			})
		case strings.Contains(r.URL.Path, "/sections/sec-todo/tasks"):
			json.NewEncoder(w).Encode(asanaTasksResponse{
				Data: []asanaTask{
					{GID: "task-1", Name: "Tagged Task", Tags: []asanaTag{{Name: "erg"}}},
					{GID: "task-2", Name: "Untagged Task", Tags: []asanaTag{}},
				},
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	// Section filter + label filter: only the tagged task should come through.
	issues, err := p.FetchIssues(context.Background(), "/repo", FilterConfig{Project: "12345", Section: "Todo", Label: "erg"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "task-1" {
		t.Errorf("expected task-1, got %s", issues[0].ID)
	}
}

func TestAsanaProvider_RemoveLabel(t *testing.T) {
	var removeTagReqBody string
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/task-gid-123"):
			// Return task tags including the one to remove.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"tags": []map[string]any{
						{"gid": "tag-gid-abc", "name": "queued"},
						{"gid": "tag-gid-xyz", "name": "other-tag"},
					},
				},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/removeTag"):
			body, _ := io.ReadAll(r.Body)
			removeTagReqBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	err := p.RemoveLabel(context.Background(), "/repo", "task-gid-123", "queued")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if requestCount != 2 {
		t.Errorf("expected 2 requests (fetch tags + remove tag), got %d", requestCount)
	}
	if !strings.Contains(removeTagReqBody, "tag-gid-abc") {
		t.Errorf("expected remove tag request to contain tag GID, got: %s", removeTagReqBody)
	}
}

func TestAsanaProvider_RemoveLabel_TagNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Task has no matching tag.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"tags": []map[string]any{
					{"gid": "tag-gid-xyz", "name": "other-tag"},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	// Should succeed (no-op) when tag is not found.
	err := p.RemoveLabel(context.Background(), "/repo", "task-gid-123", "queued")
	if err != nil {
		t.Fatalf("unexpected error when tag not found: %v", err)
	}
}

func TestAsanaProvider_RemoveLabel_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	p := NewAsanaProvider(nil)

	err := p.RemoveLabel(context.Background(), "/repo", "task-gid-123", "queued")
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_Comment(t *testing.T) {
	var storyReqBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/stories") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		storyReqBody = string(body)
		w.WriteHeader(http.StatusCreated)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gid": "story-123"}})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	err := p.Comment(context.Background(), "/repo", "task-gid-123", "Hello, world!")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(storyReqBody, "Hello, world!") {
		t.Errorf("expected story body to contain message, got: %s", storyReqBody)
	}
}

func TestAsanaProvider_Comment_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	p := NewAsanaProvider(nil)

	err := p.Comment(context.Background(), "/repo", "task-gid-123", "Hello!")
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_ImplementsProviderActions(t *testing.T) {
	var _ ProviderActions = (*AsanaProvider)(nil)
}

func TestAsanaProvider_ImplementsProviderGateChecker(t *testing.T) {
	var _ ProviderGateChecker = (*AsanaProvider)(nil)
}

func TestAsanaProvider_ImplementsProviderSectionMover(t *testing.T) {
	var _ ProviderSectionMover = (*AsanaProvider)(nil)
}

func TestAsanaProvider_ImplementsProviderSectionChecker(t *testing.T) {
	var _ ProviderSectionChecker = (*AsanaProvider)(nil)
}

// --- IsInSection tests ---

func TestAsanaProvider_IsInSection_InTargetSection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/tasks/task-gid-456") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"memberships": []map[string]any{
					{
						"project": map[string]any{"gid": "proj-123"},
						"section": map[string]any{"gid": "sec-abc", "name": "In Progress"},
					},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	inSection, err := p.IsInSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inSection {
		t.Error("expected inSection=true when task is in the target section")
	}
}

func TestAsanaProvider_IsInSection_CaseInsensitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"memberships": []map[string]any{
					{
						"project": map[string]any{"gid": "proj-123"},
						"section": map[string]any{"gid": "sec-abc", "name": "In Progress"},
					},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	inSection, err := p.IsInSection(context.Background(), "/test/repo", "task-gid-456", "IN PROGRESS")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inSection {
		t.Error("expected case-insensitive match")
	}
}

func TestAsanaProvider_IsInSection_DifferentSection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"memberships": []map[string]any{
					{
						"project": map[string]any{"gid": "proj-123"},
						"section": map[string]any{"gid": "sec-abc", "name": "Backlog"},
					},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	inSection, err := p.IsInSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inSection {
		t.Error("expected inSection=false when task is in a different section")
	}
}

func TestAsanaProvider_IsInSection_ProjectMismatch(t *testing.T) {
	// Task is in the target section but for a different project — should return false.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"memberships": []map[string]any{
					{
						"project": map[string]any{"gid": "other-proj"},
						"section": map[string]any{"gid": "sec-abc", "name": "In Progress"},
					},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123") // configured project differs
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	inSection, err := p.IsInSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inSection {
		t.Error("expected inSection=false when membership project does not match configured project")
	}
}

func TestAsanaProvider_IsInSection_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProvider(cfg)

	_, err := p.IsInSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_IsInSection_NoProjectConfigured(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{} // no project configured
	p := NewAsanaProvider(cfg)

	_, err := p.IsInSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err == nil {
		t.Error("expected error when project not configured")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected 'not configured' in error, got: %v", err)
	}
}

// --- MoveToSection tests ---

func TestAsanaProvider_MoveToSection_Success(t *testing.T) {
	var addTaskReq []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects/proj-123/sections"):
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"gid": "sec-abc", "name": "In Progress"},
					{"gid": "sec-xyz", "name": "Done"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/sections/sec-abc/addTask"):
			addTaskReq, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	err := p.MoveToSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(addTaskReq), "task-gid-456") {
		t.Errorf("expected task GID in addTask request body, got: %s", addTaskReq)
	}
}

func TestAsanaProvider_MoveToSection_CaseInsensitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects/proj-123/sections"):
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"gid": "sec-abc", "name": "In Progress"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/sections/sec-abc/addTask"):
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	// Matching "IN PROGRESS" against section named "In Progress".
	err := p.MoveToSection(context.Background(), "/test/repo", "task-gid-456", "IN PROGRESS")
	if err != nil {
		t.Fatalf("expected case-insensitive match, got error: %v", err)
	}
}

func TestAsanaProvider_MoveToSection_SectionNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"gid": "sec-abc", "name": "In Progress"},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	err := p.MoveToSection(context.Background(), "/test/repo", "task-gid-456", "Nonexistent Section")
	if err == nil {
		t.Error("expected error for section not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestAsanaProvider_MoveToSection_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	cfg := &config.Config{}
	cfg.SetAsanaProject("/test/repo", "proj-123")
	p := NewAsanaProvider(cfg)

	err := p.MoveToSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_MoveToSection_NoProjectConfigured(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{} // no project configured
	p := NewAsanaProvider(cfg)

	err := p.MoveToSection(context.Background(), "/test/repo", "task-gid-456", "In Progress")
	if err == nil {
		t.Error("expected error when project not configured")
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Errorf("expected 'not configured' in error, got: %v", err)
	}
}

// --- CheckIssueHasLabel tests ---

func TestAsanaProvider_CheckIssueHasLabel_Found(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/tasks/task-gid-123") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"tags": []map[string]any{
					{"name": "approved"},
					{"name": "other-tag"},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	has, err := p.CheckIssueHasLabel(context.Background(), "/repo", "task-gid-123", "approved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !has {
		t.Error("expected has=true when label is present")
	}
}

func TestAsanaProvider_CheckIssueHasLabel_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"tags": []map[string]any{
					{"name": "other-tag"},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	has, err := p.CheckIssueHasLabel(context.Background(), "/repo", "task-gid-123", "approved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Error("expected has=false when label is absent")
	}
}

func TestAsanaProvider_CheckIssueHasLabel_CaseInsensitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"tags": []map[string]any{
					{"name": "Approved"},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	has, err := p.CheckIssueHasLabel(context.Background(), "/repo", "task-gid-123", "approved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !has {
		t.Error("expected has=true for case-insensitive label match")
	}
}

func TestAsanaProvider_CheckIssueHasLabel_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	p := NewAsanaProvider(nil)

	_, err := p.CheckIssueHasLabel(context.Background(), "/repo", "task-gid-123", "approved")
	if err == nil {
		t.Error("expected error without PAT")
	}
}

// --- GetIssueComments tests ---

func TestAsanaProvider_GetIssueComments_ReturnsComments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/tasks/task-gid-123/stories") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"type":       "comment",
					"text":       "LGTM, looks good!",
					"created_at": "2024-01-15T10:00:00Z",
					"created_by": map[string]any{"name": "alice"},
				},
				{
					"type":       "system",
					"text":       "Task moved to In Progress",
					"created_at": "2024-01-15T09:00:00Z",
					"created_by": map[string]any{"name": "asana-bot"},
				},
				{
					"type":       "comment",
					"text":       "Please add more detail",
					"created_at": "2024-01-14T08:00:00Z",
					"created_by": map[string]any{"name": "bob"},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	comments, err := p.GetIssueComments(context.Background(), "/repo", "task-gid-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only comment-type stories should be returned; system story is skipped.
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].Author != "alice" {
		t.Errorf("expected author 'alice', got %q", comments[0].Author)
	}
	if comments[0].Body != "LGTM, looks good!" {
		t.Errorf("expected body 'LGTM, looks good!', got %q", comments[0].Body)
	}
	if comments[0].CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be non-zero")
	}
	if comments[1].Author != "bob" {
		t.Errorf("expected author 'bob', got %q", comments[1].Author)
	}
}

func TestAsanaProvider_GetIssueComments_EmptyBodyExcluded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"type": "comment", "text": "", "created_at": "2024-01-15T10:00:00Z", "created_by": map[string]any{"name": "alice"}},
				{"type": "comment", "text": "real comment", "created_at": "2024-01-15T11:00:00Z", "created_by": map[string]any{"name": "bob"}},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	comments, err := p.GetIssueComments(context.Background(), "/repo", "task-gid-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment (empty body excluded), got %d", len(comments))
	}
	if comments[0].Author != "bob" {
		t.Errorf("expected author 'bob', got %q", comments[0].Author)
	}
}

func TestAsanaProvider_GetIssueComments_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	p := NewAsanaProvider(nil)

	_, err := p.GetIssueComments(context.Background(), "/repo", "task-gid-123")
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_FetchProjects_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)

	os.Setenv(asanaPATEnvVar, "")

	p := NewAsanaProvider(nil)
	ctx := context.Background()
	_, err := p.FetchProjects(ctx)
	if err == nil {
		t.Error("expected error without PAT")
	}
}

func TestAsanaProvider_FetchProjects_SingleWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/workspaces":
			json.NewEncoder(w).Encode(asanaWorkspacesResponse{
				Data: []asanaWorkspace{
					{GID: "ws1", Name: "My Workspace"},
				},
			})
		case "/workspaces/ws1/projects":
			json.NewEncoder(w).Encode(asanaProjectsResponse{
				Data: []asanaProject{
					{GID: "p1", Name: "Project Alpha"},
					{GID: "p2", Name: "Project Beta"},
				},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	projects, err := p.FetchProjects(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	// Single workspace: names should NOT be prefixed
	if projects[0].Name != "Project Alpha" {
		t.Errorf("expected name 'Project Alpha', got %q", projects[0].Name)
	}
	if projects[0].GID != "p1" {
		t.Errorf("expected GID 'p1', got %q", projects[0].GID)
	}
	if projects[1].Name != "Project Beta" {
		t.Errorf("expected name 'Project Beta', got %q", projects[1].Name)
	}
}

func TestAsanaProvider_FetchProjects_MultipleWorkspaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/workspaces":
			json.NewEncoder(w).Encode(asanaWorkspacesResponse{
				Data: []asanaWorkspace{
					{GID: "ws1", Name: "Workspace A"},
					{GID: "ws2", Name: "Workspace B"},
				},
			})
		case "/workspaces/ws1/projects":
			json.NewEncoder(w).Encode(asanaProjectsResponse{
				Data: []asanaProject{
					{GID: "p1", Name: "Alpha"},
				},
			})
		case "/workspaces/ws2/projects":
			json.NewEncoder(w).Encode(asanaProjectsResponse{
				Data: []asanaProject{
					{GID: "p2", Name: "Beta"},
				},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	projects, err := p.FetchProjects(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	// Multiple workspaces: names should be prefixed
	if projects[0].Name != "Workspace A / Alpha" {
		t.Errorf("expected name 'Workspace A / Alpha', got %q", projects[0].Name)
	}
	if projects[1].Name != "Workspace B / Beta" {
		t.Errorf("expected name 'Workspace B / Beta', got %q", projects[1].Name)
	}
}

func TestAsanaProvider_FetchProjects_EmptyWorkspaces(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(asanaWorkspacesResponse{Data: []asanaWorkspace{}})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	projects, err := p.FetchProjects(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if projects != nil {
		t.Errorf("expected nil projects for empty workspaces, got %v", projects)
	}
}

func TestAsanaProvider_FetchProjects_WorkspacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	_, err := p.FetchProjects(ctx)
	if err == nil {
		t.Error("expected error from API error response")
	}
}

func TestAsanaProvider_FetchProjects_Pagination(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/workspaces":
			json.NewEncoder(w).Encode(asanaWorkspacesResponse{
				Data: []asanaWorkspace{
					{GID: "ws1", Name: "My Workspace"},
				},
			})
		case "/workspaces/ws1/projects":
			offset := r.URL.Query().Get("offset")
			requestCount++
			if offset == "" {
				// First page
				json.NewEncoder(w).Encode(asanaProjectsResponse{
					Data: []asanaProject{
						{GID: "p1", Name: "Project 1"},
						{GID: "p2", Name: "Project 2"},
					},
					NextPage: &asanaNextPage{
						Offset: "page2token",
					},
				})
			} else if offset == "page2token" {
				// Second page
				json.NewEncoder(w).Encode(asanaProjectsResponse{
					Data: []asanaProject{
						{GID: "p3", Name: "Project 3"},
					},
					NextPage: nil, // No more pages
				})
			} else {
				http.Error(w, "unexpected offset", http.StatusBadRequest)
			}
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	projects, err := p.FetchProjects(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects across 2 pages, got %d", len(projects))
	}
	if projects[0].Name != "Project 1" {
		t.Errorf("expected 'Project 1', got %q", projects[0].Name)
	}
	if projects[1].Name != "Project 2" {
		t.Errorf("expected 'Project 2', got %q", projects[1].Name)
	}
	if projects[2].Name != "Project 3" {
		t.Errorf("expected 'Project 3', got %q", projects[2].Name)
	}
	// Should have made 2 requests for projects (page 1 + page 2)
	if requestCount != 2 {
		t.Errorf("expected 2 project requests, got %d", requestCount)
	}
}

func TestAsanaProvider_FetchProjects_ProjectsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/workspaces":
			json.NewEncoder(w).Encode(asanaWorkspacesResponse{
				Data: []asanaWorkspace{{GID: "ws1", Name: "WS"}},
			})
		default:
			http.Error(w, "Forbidden", http.StatusForbidden)
		}
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	_, err := p.FetchProjects(ctx)
	if err == nil {
		t.Error("expected error from projects API error")
	}
}

func TestAsanaProvider_FetchIssues_TagFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify tags.name is included in opt_fields
		optFields := r.URL.Query().Get("opt_fields")
		if !strings.Contains(optFields, "tags.name") {
			t.Errorf("expected opt_fields to contain 'tags.name', got %q", optFields)
		}

		response := asanaTasksResponse{
			Data: []asanaTask{
				{
					GID: "1", Name: "Task with queued tag", Notes: "desc1",
					Permalink: "https://app.asana.com/0/123/1",
					Tags:      []asanaTag{{Name: "queued"}},
				},
				{
					GID: "2", Name: "Task with other tag", Notes: "desc2",
					Permalink: "https://app.asana.com/0/123/2",
					Tags:      []asanaTag{{Name: "other"}},
				},
				{
					GID: "3", Name: "Task with no tags", Notes: "desc3",
					Permalink: "https://app.asana.com/0/123/3",
					Tags:      nil,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Project: "12345", Label: "queued"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "1" {
		t.Errorf("expected issue ID '1', got %q", issues[0].ID)
	}
	if issues[0].Title != "Task with queued tag" {
		t.Errorf("expected title 'Task with queued tag', got %q", issues[0].Title)
	}
}

func TestAsanaProvider_FetchIssues_TagFilterCaseInsensitive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := asanaTasksResponse{
			Data: []asanaTask{
				{
					GID: "1", Name: "Task with Queued tag", Notes: "desc1",
					Permalink: "https://app.asana.com/0/123/1",
					Tags:      []asanaTag{{Name: "Queued"}},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Project: "12345", Label: "queued"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (case-insensitive match), got %d", len(issues))
	}
	if issues[0].ID != "1" {
		t.Errorf("expected issue ID '1', got %q", issues[0].ID)
	}
}

func TestAsanaProvider_FetchIssues_NoLabelReturnsAll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := asanaTasksResponse{
			Data: []asanaTask{
				{
					GID: "1", Name: "Task with tag", Notes: "desc1",
					Permalink: "https://app.asana.com/0/123/1",
					Tags:      []asanaTag{{Name: "queued"}},
				},
				{
					GID: "2", Name: "Task without tag", Notes: "desc2",
					Permalink: "https://app.asana.com/0/123/2",
					Tags:      nil,
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	cfg := &config.Config{}
	p := NewAsanaProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Project: "12345"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues (no label filter), got %d", len(issues))
	}
}

func TestAsanaProvider_GetIssueComments_IncludesGID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"gid":        "story-gid-1",
					"type":       "comment",
					"text":       "First comment [erg:step=notify]",
					"created_at": "2024-01-01T10:00:00Z",
					"created_by": map[string]any{"name": "Bot"},
				},
				{
					"gid":        "story-gid-2",
					"type":       "comment",
					"text":       "Second comment",
					"created_at": "2024-01-01T11:00:00Z",
					"created_by": map[string]any{"name": "User"},
				},
			},
		})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)
	comments, err := p.GetIssueComments(context.Background(), "/repo", "task-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	if comments[0].ID != "story-gid-1" {
		t.Errorf("expected ID 'story-gid-1', got %q", comments[0].ID)
	}
	if !strings.Contains(comments[0].Body, "[erg:step=notify]") {
		t.Errorf("expected marker in comment body, got %q", comments[0].Body)
	}
	if comments[1].ID != "story-gid-2" {
		t.Errorf("expected ID 'story-gid-2', got %q", comments[1].ID)
	}
}

func TestAsanaProvider_UpdateComment_Success(t *testing.T) {
	var capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/stories/story-gid-1") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)
	err := p.UpdateComment(context.Background(), "/repo", "task-123", "story-gid-1", "Updated body [erg:step=notify]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedBody, "Updated body") {
		t.Errorf("expected updated body in request, got: %s", capturedBody)
	}
}

func TestAsanaProvider_UpdateComment_NoPAT(t *testing.T) {
	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "")

	p := NewAsanaProvider(nil)
	err := p.UpdateComment(context.Background(), "/repo", "task-123", "story-gid-1", "body")
	if err == nil {
		t.Fatal("expected error without PAT")
	}
}

func TestAsanaProvider_UpdateComment_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer server.Close()

	origPAT := os.Getenv(asanaPATEnvVar)
	defer os.Setenv(asanaPATEnvVar, origPAT)
	os.Setenv(asanaPATEnvVar, "test-pat")

	p := NewAsanaProviderWithClient(nil, server.Client(), server.URL)
	err := p.UpdateComment(context.Background(), "/repo", "task-123", "story-gid-1", "body")
	if err == nil {
		t.Fatal("expected error on server error response")
	}
}
