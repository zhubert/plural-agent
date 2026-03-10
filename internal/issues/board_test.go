package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListAsanaSections(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/proj123/sections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(asanaSectionsResponse{
			Data: []asanaSection{
				{GID: "s1", Name: "To do"},
				{GID: "s2", Name: "Doing"},
				{GID: "s3", Name: "Done"},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("ASANA_PAT", "test-token")
	origBase := boardAsanaBase
	boardAsanaBase = srv.URL
	defer func() { boardAsanaBase = origBase }()

	sections, err := ListAsanaSections(context.Background(), "proj123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(sections))
	}
	if sections[0].Name != "To do" {
		t.Errorf("expected first section 'To do', got %q", sections[0].Name)
	}
	if sections[1].ID != "s2" {
		t.Errorf("expected second section ID 's2', got %q", sections[1].ID)
	}
}

func TestListAsanaSections_NoToken(t *testing.T) {
	t.Setenv("ASANA_PAT", "")
	origKeychainGet := keychainGet
	keychainGet = func(string) (string, bool) { return "", false }
	defer func() { keychainGet = origKeychainGet }()

	_, err := ListAsanaSections(context.Background(), "proj123")
	if err == nil {
		t.Fatal("expected error when no token available")
	}
}

func TestCreateAsanaSection(t *testing.T) {
	var capturedBody struct {
		Data struct {
			Name         string `json:"name"`
			InsertBefore string `json:"insert_before"`
		} `json:"data"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/projects/proj123/sections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"data":{"gid":"new-gid","name":"Doing"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("ASANA_PAT", "test-token")
	origBase := boardAsanaBase
	boardAsanaBase = srv.URL
	defer func() { boardAsanaBase = origBase }()

	// Without insert_before
	err := CreateAsanaSection(context.Background(), "proj123", "Doing", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedBody.Data.Name != "Doing" {
		t.Errorf("expected name 'Doing', got %q", capturedBody.Data.Name)
	}

	// With insert_before
	err = CreateAsanaSection(context.Background(), "proj123", "In Review", "done-gid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedBody.Data.Name != "In Review" {
		t.Errorf("expected name 'In Review', got %q", capturedBody.Data.Name)
	}
	if capturedBody.Data.InsertBefore != "done-gid" {
		t.Errorf("expected insert_before 'done-gid', got %q", capturedBody.Data.InsertBefore)
	}
}

func TestListLinearWorkflowStates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"team": map[string]any{
					"states": map[string]any{
						"nodes": []map[string]string{
							{"id": "s1", "name": "Backlog"},
							{"id": "s2", "name": "In Progress"},
							{"id": "s3", "name": "Done"},
						},
					},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("LINEAR_API_KEY", "test-key")
	origBase := boardLinearBase
	boardLinearBase = srv.URL
	defer func() { boardLinearBase = origBase }()

	states, err := ListLinearWorkflowStates(context.Background(), "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(states) != 3 {
		t.Fatalf("expected 3 states, got %d", len(states))
	}
	if states[1].Name != "In Progress" {
		t.Errorf("expected second state 'In Progress', got %q", states[1].Name)
	}
}

func TestCreateLinearWorkflowState(t *testing.T) {
	var capturedVars map[string]any

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var gql struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&gql)
		capturedVars = gql.Variables

		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"workflowStateCreate": map[string]any{
					"success": true,
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("LINEAR_API_KEY", "test-key")
	origBase := boardLinearBase
	boardLinearBase = srv.URL
	defer func() { boardLinearBase = origBase }()

	err := CreateLinearWorkflowState(context.Background(), "team1", "In Review", "started", "#4ea7fc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedVars["name"] != "In Review" {
		t.Errorf("expected name 'In Review', got %v", capturedVars["name"])
	}
	if capturedVars["type"] != "started" {
		t.Errorf("expected type 'started', got %v", capturedVars["type"])
	}
	if capturedVars["color"] != "#4ea7fc" {
		t.Errorf("expected color '#4ea7fc', got %v", capturedVars["color"])
	}
}

func TestCreateLinearWorkflowState_Failure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"workflowStateCreate": map[string]any{
					"success": false,
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("LINEAR_API_KEY", "test-key")
	origBase := boardLinearBase
	boardLinearBase = srv.URL
	defer func() { boardLinearBase = origBase }()

	err := CreateLinearWorkflowState(context.Background(), "team1", "Bad State", "started", "#000000")
	if err == nil {
		t.Fatal("expected error on success=false")
	}
}

func TestFindSectionByName(t *testing.T) {
	sections := []BoardSection{
		{ID: "1", Name: "To do"},
		{ID: "2", Name: "Doing"},
		{ID: "3", Name: "Done"},
	}

	// Exact match
	s, found := FindSectionByName(sections, "Doing")
	if !found {
		t.Fatal("expected to find 'Doing'")
	}
	if s.ID != "2" {
		t.Errorf("expected ID '2', got %q", s.ID)
	}

	// Case-insensitive
	s, found = FindSectionByName(sections, "doing")
	if !found {
		t.Fatal("expected case-insensitive match for 'doing'")
	}
	if s.ID != "2" {
		t.Errorf("expected ID '2', got %q", s.ID)
	}

	// Not found
	_, found = FindSectionByName(sections, "In Review")
	if found {
		t.Error("expected 'In Review' to not be found")
	}

	// Empty list
	_, found = FindSectionByName(nil, "anything")
	if found {
		t.Error("expected not found on empty list")
	}
}
