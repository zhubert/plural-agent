package issues

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIRequest_Success(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("auth header = %q, want %q", got, "Bearer tok")
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("accept header = %q, want %q", got, "application/json")
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(payload{Name: "hello"})
	}))
	defer srv.Close()

	var result payload
	err := apiRequest(context.Background(), srv.Client(), http.MethodGet, srv.URL, nil,
		"Bearer tok", http.StatusOK, "", "Test", &result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != "hello" {
		t.Errorf("result.Name = %q, want %q", result.Name, "hello")
	}
}

func TestAPIRequest_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := apiRequest(context.Background(), srv.Client(), http.MethodGet, srv.URL, nil,
		"Bearer tok", http.StatusOK,
		"check your credentials", "Test", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "check your credentials") {
		t.Errorf("error = %q, want it to contain %q", err, "check your credentials")
	}
}

func TestAPIRequest_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := apiRequest(context.Background(), srv.Client(), http.MethodGet, srv.URL, nil,
		"Bearer tok", http.StatusOK, "", "TestProvider", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "TestProvider API returned status 500") {
		t.Errorf("error = %q, want it to contain status message", err)
	}
}

func TestAPIRequest_DecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	var result struct{ Name string }
	err := apiRequest(context.Background(), srv.Client(), http.MethodGet, srv.URL, nil,
		"Bearer tok", http.StatusOK, "", "Test", &result)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("error = %q, want it to contain 'failed to parse'", err)
	}
}

func TestAPIRequest_PostSetsContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want %q", got, "application/json")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := strings.NewReader(`{"key":"value"}`)
	err := apiRequest(context.Background(), srv.Client(), http.MethodPost, srv.URL, body,
		"rawkey", http.StatusOK, "", "Test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
