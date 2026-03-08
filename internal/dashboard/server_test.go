package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockController is a SessionController implementation for tests.
type mockController struct {
	stopErr   error
	retryErr  error
	msgErr    error
	stopCalls []string
	retryCalls []string
	msgCalls  []struct{ itemID, msg string }
}

func (m *mockController) StopSession(itemID string) error {
	m.stopCalls = append(m.stopCalls, itemID)
	return m.stopErr
}
func (m *mockController) RetryWorkItem(itemID string) error {
	m.retryCalls = append(m.retryCalls, itemID)
	return m.retryErr
}
func (m *mockController) SendMessage(itemID, message string) error {
	m.msgCalls = append(m.msgCalls, struct{ itemID, msg string }{itemID, message})
	return m.msgErr
}

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
	// Verify error body doesn't leak filesystem paths
	body := w.Body.String()
	if strings.Contains(body, "/") {
		t.Errorf("error response should not contain paths, got: %s", body)
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

// ---- SessionController / control endpoints ----

func TestHandleCapabilities_NoController(t *testing.T) {
	srv := New("localhost:0")
	req := httptest.NewRequest("GET", "/api/capabilities", nil)
	w := httptest.NewRecorder()
	srv.handleCapabilities(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var caps map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("failed to decode capabilities: %v", err)
	}
	if caps["control"] {
		t.Error("expected control=false with no controller")
	}
}

func TestHandleCapabilities_WithController(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	req := httptest.NewRequest("GET", "/api/capabilities", nil)
	w := httptest.NewRecorder()
	srv.handleCapabilities(w, req)

	var caps map[string]bool
	json.NewDecoder(w.Result().Body).Decode(&caps)
	if !caps["control"] {
		t.Error("expected control=true with controller set")
	}
}

func TestHandleStop_NoController(t *testing.T) {
	srv := New("localhost:0")
	req := httptest.NewRequest("POST", "/api/workitems/item-1/stop", nil)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleStop(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Result().StatusCode)
	}
}

func TestHandleStop_Success(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	req := httptest.NewRequest("POST", "/api/workitems/item-1/stop", nil)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleStop(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
	if len(ctrl.stopCalls) != 1 || ctrl.stopCalls[0] != "item-1" {
		t.Errorf("expected StopSession(item-1), got %v", ctrl.stopCalls)
	}
}

func TestHandleStop_ControllerError(t *testing.T) {
	ctrl := &mockController{stopErr: fmt.Errorf("worker not found")}
	srv := New("localhost:0", WithController(ctrl))
	req := httptest.NewRequest("POST", "/api/workitems/item-1/stop", nil)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleStop(w, req)

	if w.Result().StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Result().StatusCode)
	}
}

func TestHandleRetry_NoController(t *testing.T) {
	srv := New("localhost:0")
	req := httptest.NewRequest("POST", "/api/workitems/item-1/retry", nil)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Result().StatusCode)
	}
}

func TestHandleRetry_Success(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	req := httptest.NewRequest("POST", "/api/workitems/item-2/retry", nil)
	req.SetPathValue("itemID", "item-2")
	w := httptest.NewRecorder()
	srv.handleRetry(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
	if len(ctrl.retryCalls) != 1 || ctrl.retryCalls[0] != "item-2" {
		t.Errorf("expected RetryWorkItem(item-2), got %v", ctrl.retryCalls)
	}
}

func TestHandleMessage_NoController(t *testing.T) {
	srv := New("localhost:0")
	body := bytes.NewBufferString(`{"message":"hello"}`)
	req := httptest.NewRequest("POST", "/api/workitems/item-1/message", body)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleMessage(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Result().StatusCode)
	}
}

func TestHandleMessage_Success(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	body := bytes.NewBufferString(`{"message":"do the thing"}`)
	req := httptest.NewRequest("POST", "/api/workitems/item-3/message", body)
	req.SetPathValue("itemID", "item-3")
	w := httptest.NewRecorder()
	srv.handleMessage(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Result().StatusCode)
	}
	if len(ctrl.msgCalls) != 1 || ctrl.msgCalls[0].itemID != "item-3" || ctrl.msgCalls[0].msg != "do the thing" {
		t.Errorf("unexpected msgCalls: %v", ctrl.msgCalls)
	}
}

func TestHandleMessage_EmptyMessage(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	body := bytes.NewBufferString(`{"message":""}`)
	req := httptest.NewRequest("POST", "/api/workitems/item-1/message", body)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleMessage(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty message, got %d", w.Result().StatusCode)
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest("POST", "/api/workitems/item-1/message", body)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleMessage(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Result().StatusCode)
	}
}

func TestNew_WithController(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	if srv.controller == nil {
		t.Error("expected controller to be set")
	}
}

func TestNew_NoController(t *testing.T) {
	srv := New("localhost:0")
	if srv.controller != nil {
		t.Error("expected controller to be nil by default")
	}
}
