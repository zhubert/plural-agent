package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleIndex(t *testing.T) {
	srv := New("localhost:0")

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	srv.handleIndex(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html, got %s", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "erg dashboard") {
		t.Error("expected HTML to contain 'erg dashboard'")
	}
}

func TestHandleState(t *testing.T) {
	srv := New("localhost:0")

	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()

	srv.handleState(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if snap.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestHandleSSE(t *testing.T) {
	srv := New("localhost:0")

	// Create a test server to properly handle SSE
	ts := httptest.NewServer(http.HandlerFunc(srv.handleSSE))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	// Read the initial SSE event
	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("failed to read SSE: %v", err)
	}

	data := string(buf[:n])
	if !strings.HasPrefix(data, "data: ") {
		t.Errorf("expected SSE data prefix, got: %s", data[:min(50, len(data))])
	}
}

func TestHandleLogs_NoSession(t *testing.T) {
	srv := New("localhost:0")

	req := httptest.NewRequest("GET", "/api/logs/nonexistent-session-id", nil)
	req.SetPathValue("sessionID", "nonexistent-session-id")
	w := httptest.NewRecorder()

	srv.handleLogs(w, req)

	resp := w.Result()
	// Should get 404 since no log file exists
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleLogs_PathTraversal(t *testing.T) {
	srv := New("localhost:0")

	tests := []struct {
		name      string
		sessionID string
	}{
		{"dot-dot-slash", "../../etc/passwd"},
		{"slash", "foo/bar"},
		{"backslash", "foo\\bar"},
		{"dot-dot-only", ".."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/logs/x", nil)
			req.SetPathValue("sessionID", tt.sessionID)
			w := httptest.NewRecorder()

			srv.handleLogs(w, req)

			resp := w.Result()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("expected 400 for session ID %q, got %d", tt.sessionID, resp.StatusCode)
			}
		})
	}
}

func TestBroadcast(t *testing.T) {
	srv := New("localhost:0")

	ch := make(chan []byte, 2)
	srv.addClient(ch)

	msg := []byte(`{"test": true}`)
	srv.broadcast(msg)

	select {
	case got := <-ch:
		if string(got) != string(msg) {
			t.Errorf("expected %s, got %s", msg, got)
		}
	default:
		t.Error("expected message on channel")
	}

	srv.removeClient(ch)

	// Verify channel is closed
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed")
	}
}

func TestBroadcast_SlowClient(t *testing.T) {
	srv := New("localhost:0")

	// Channel with buffer of 1
	ch := make(chan []byte, 1)
	srv.addClient(ch)

	// Fill the buffer
	ch <- []byte("fill")

	// Broadcast should not block
	done := make(chan struct{})
	go func() {
		srv.broadcast([]byte("test"))
		close(done)
	}()

	select {
	case <-done:
		// Good, didn't block
	case <-time.After(time.Second):
		t.Fatal("broadcast blocked on slow client")
	}

	srv.removeClient(ch)
}
