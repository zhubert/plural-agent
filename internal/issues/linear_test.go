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

func TestLinearProvider_Name(t *testing.T) {
	p := NewLinearProvider(nil)
	if p.Name() != "Linear Issues" {
		t.Errorf("expected 'Linear Issues', got '%s'", p.Name())
	}
}

func TestLinearProvider_Source(t *testing.T) {
	p := NewLinearProvider(nil)
	if p.Source() != SourceLinear {
		t.Errorf("expected SourceLinear, got '%s'", p.Source())
	}
}

func TestLinearProvider_IsConfigured(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetLinearTeam("/test/repo", "team-123")

	p := NewLinearProvider(cfg)

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)

	// Test without API key
	os.Setenv(linearAPIKeyEnvVar, "")
	if p.IsConfigured("/test/repo") {
		t.Error("expected IsConfigured=false without API key")
	}

	// Test with API key but without team mapping
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")
	if p.IsConfigured("/other/repo") {
		t.Error("expected IsConfigured=false without team mapping")
	}

	// Test with both API key and team mapping
	if !p.IsConfigured("/test/repo") {
		t.Error("expected IsConfigured=true with API key and team mapping")
	}
}

func TestLinearProvider_GenerateBranchName(t *testing.T) {
	p := NewLinearProvider(nil)

	tests := []struct {
		name     string
		issue    Issue
		expected string
	}{
		{"simple identifier", Issue{ID: "ENG-123"}, "linear-eng-123"},
		{"uppercase identifier", Issue{ID: "PROJ-456"}, "linear-proj-456"},
		{"already lowercase", Issue{ID: "eng-789"}, "linear-eng-789"},
		{"mixed case", Issue{ID: "Dev-42"}, "linear-dev-42"},
		{"long identifier", Issue{ID: "ENGINEERING-99999"}, "linear-engineering-99999"},
		{"single char prefix", Issue{ID: "X-1"}, "linear-x-1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.GenerateBranchName(tc.issue)
			if result != tc.expected {
				t.Errorf("GenerateBranchName(%q) = %s, expected %s", tc.issue.ID, result, tc.expected)
			}
		})
	}
}

func TestLinearProvider_GetPRLinkText(t *testing.T) {
	p := NewLinearProvider(nil)

	tests := []struct {
		name     string
		issue    Issue
		expected string
	}{
		{"standard identifier", Issue{ID: "ENG-123", Source: SourceLinear}, "Fixes ENG-123"},
		{"different prefix", Issue{ID: "PROJ-456", Source: SourceLinear}, "Fixes PROJ-456"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := p.GetPRLinkText(tc.issue)
			if result != tc.expected {
				t.Errorf("GetPRLinkText(%q) = %s, expected %s", tc.issue.ID, result, tc.expected)
			}
		})
	}
}

func TestLinearProvider_FetchIssues_NoAPIKey(t *testing.T) {
	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)

	os.Setenv(linearAPIKeyEnvVar, "")

	cfg := &config.Config{}
	p := NewLinearProvider(cfg)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Team: "team-123"})
	if err == nil {
		t.Error("expected error without API key")
	}
}

func TestLinearProvider_FetchIssues_NoTeamID(t *testing.T) {
	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)

	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	cfg := &config.Config{}
	p := NewLinearProvider(cfg)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{})
	if err == nil {
		t.Error("expected error without team ID")
	}
}

func TestLinearProvider_FetchIssues_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header (Linear uses plain API key, not Bearer)
		auth := r.Header.Get("Authorization")
		if auth != "lin_api_test123" {
			t.Errorf("expected 'lin_api_test123', got '%s'", auth)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Verify Content-Type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type 'application/json', got '%s'", r.Header.Get("Content-Type"))
		}

		// Verify it's a POST to /graphql
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/graphql" {
			t.Errorf("expected /graphql, got %s", r.URL.Path)
		}

		// Verify request body contains the team ID variable
		body, _ := io.ReadAll(r.Body)
		var gqlReq linearGraphQLRequest
		json.Unmarshal(body, &gqlReq)
		if gqlReq.Variables["teamId"] != "team-123" {
			t.Errorf("expected teamId 'team-123', got '%v'", gqlReq.Variables["teamId"])
		}

		response := linearTeamIssuesResponse{}
		response.Data.Team.Issues.Nodes = []linearIssue{
			{ID: "uuid-1", Identifier: "ENG-123", Title: "Fix login bug", Description: "Login fails on mobile", URL: "https://linear.app/team/issue/ENG-123"},
			{ID: "uuid-2", Identifier: "ENG-456", Title: "Add dark mode", Description: "Implement dark mode toggle", URL: "https://linear.app/team/issue/ENG-456"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	cfg := &config.Config{}
	p := NewLinearProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Team: "team-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	// Verify identifier is used as ID
	if issues[0].ID != "ENG-123" {
		t.Errorf("expected ID 'ENG-123', got %q", issues[0].ID)
	}
	if issues[0].Title != "Fix login bug" {
		t.Errorf("expected title 'Fix login bug', got %q", issues[0].Title)
	}
	if issues[0].Body != "Login fails on mobile" {
		t.Errorf("expected body 'Login fails on mobile', got %q", issues[0].Body)
	}
	if issues[0].URL != "https://linear.app/team/issue/ENG-123" {
		t.Errorf("expected URL 'https://linear.app/team/issue/ENG-123', got %q", issues[0].URL)
	}
	if issues[0].Source != SourceLinear {
		t.Errorf("expected source SourceLinear, got %q", issues[0].Source)
	}
	if issues[1].ID != "ENG-456" {
		t.Errorf("expected ID 'ENG-456', got %q", issues[1].ID)
	}
}

func TestLinearProvider_FetchIssues_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	cfg := &config.Config{}
	p := NewLinearProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Team: "team-123"})
	if err == nil {
		t.Error("expected error from API error response")
	}
}

func TestLinearProvider_FetchIssues_Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	cfg := &config.Config{}
	p := NewLinearProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	_, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Team: "team-123"})
	if err == nil {
		t.Error("expected error from 403 response")
	}
	if err != nil && !contains(err.Error(), "403 Forbidden") {
		t.Errorf("expected error to mention 403 Forbidden, got: %v", err)
	}
}

func TestLinearProvider_FetchTeams_NoAPIKey(t *testing.T) {
	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)

	os.Setenv(linearAPIKeyEnvVar, "")

	p := NewLinearProvider(nil)
	ctx := context.Background()
	_, err := p.FetchTeams(ctx)
	if err == nil {
		t.Error("expected error without API key")
	}
}

func TestLinearProvider_FetchTeams_MockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header
		auth := r.Header.Get("Authorization")
		if auth != "lin_api_test123" {
			t.Errorf("expected 'lin_api_test123', got '%s'", auth)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Verify it's a POST to /graphql
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		response := linearTeamsResponse{}
		response.Data.Teams.Nodes = []linearTeam{
			{ID: "team-1", Name: "Engineering"},
			{ID: "team-2", Name: "Design"},
			{ID: "team-3", Name: "Product"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	p := NewLinearProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	teams, err := p.FetchTeams(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(teams) != 3 {
		t.Fatalf("expected 3 teams, got %d", len(teams))
	}
	if teams[0].ID != "team-1" {
		t.Errorf("expected ID 'team-1', got %q", teams[0].ID)
	}
	if teams[0].Name != "Engineering" {
		t.Errorf("expected name 'Engineering', got %q", teams[0].Name)
	}
	if teams[1].Name != "Design" {
		t.Errorf("expected name 'Design', got %q", teams[1].Name)
	}
	if teams[2].Name != "Product" {
		t.Errorf("expected name 'Product', got %q", teams[2].Name)
	}
}

func TestLinearProvider_FetchTeams_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	p := NewLinearProviderWithClient(nil, server.Client(), server.URL)

	ctx := context.Background()
	_, err := p.FetchTeams(ctx)
	if err == nil {
		t.Error("expected error from API error response")
	}
}

func TestLinearProvider_FetchIssues_LabelFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)

		// Verify the GraphQL query contains a labels filter
		if !strings.Contains(bodyStr, "labels") {
			t.Error("expected GraphQL query to contain 'labels' filter when Label is set")
		}

		// Verify the variables include the label
		var gqlReq linearGraphQLRequest
		json.Unmarshal(body, &gqlReq)
		if gqlReq.Variables["label"] != "queued" {
			t.Errorf("expected variable 'label' to be 'queued', got '%v'", gqlReq.Variables["label"])
		}
		if gqlReq.Variables["teamId"] != "team-123" {
			t.Errorf("expected variable 'teamId' to be 'team-123', got '%v'", gqlReq.Variables["teamId"])
		}

		response := linearTeamIssuesResponse{}
		response.Data.Team.Issues.Nodes = []linearIssue{
			{ID: "uuid-1", Identifier: "ENG-100", Title: "Queued task", Description: "A queued task", URL: "https://linear.app/team/issue/ENG-100"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	cfg := &config.Config{}
	p := NewLinearProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Team: "team-123", Label: "queued"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].ID != "ENG-100" {
		t.Errorf("expected ID 'ENG-100', got %q", issues[0].ID)
	}
}

func TestLinearProvider_FetchIssues_NoLabelOmitsFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)

		// Verify the GraphQL query does NOT contain a labels filter
		if strings.Contains(bodyStr, "labels") {
			t.Error("expected GraphQL query to NOT contain 'labels' filter when Label is empty")
		}

		// Verify no label variable is sent
		var gqlReq linearGraphQLRequest
		json.Unmarshal(body, &gqlReq)
		if _, ok := gqlReq.Variables["label"]; ok {
			t.Error("expected no 'label' variable when Label is empty")
		}

		response := linearTeamIssuesResponse{}
		response.Data.Team.Issues.Nodes = []linearIssue{
			{ID: "uuid-1", Identifier: "ENG-200", Title: "Task one", Description: "First task", URL: "https://linear.app/team/issue/ENG-200"},
			{ID: "uuid-2", Identifier: "ENG-201", Title: "Task two", Description: "Second task", URL: "https://linear.app/team/issue/ENG-201"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	origKey := os.Getenv(linearAPIKeyEnvVar)
	defer os.Setenv(linearAPIKeyEnvVar, origKey)
	os.Setenv(linearAPIKeyEnvVar, "lin_api_test123")

	cfg := &config.Config{}
	p := NewLinearProviderWithClient(cfg, server.Client(), server.URL)

	ctx := context.Background()
	issues, err := p.FetchIssues(ctx, "/test/repo", FilterConfig{Team: "team-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].ID != "ENG-200" {
		t.Errorf("expected ID 'ENG-200', got %q", issues[0].ID)
	}
	if issues[1].ID != "ENG-201" {
		t.Errorf("expected ID 'ENG-201', got %q", issues[1].ID)
	}
}

// contains checks if a string contains a substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
