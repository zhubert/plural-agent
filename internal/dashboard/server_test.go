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

	iexec "github.com/zhubert/erg/internal/exec"
)

// mockController is a SessionController implementation for tests.
type mockController struct {
	stopErr    error
	retryErr   error
	msgErr     error
	stopCalls  []string
	retryCalls []string
	msgCalls   []struct{ itemID, msg string }
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
	if !strings.Contains(body, "erg orchestrator dashboard") {
		t.Error("expected HTML to contain 'erg orchestrator dashboard'")
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

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var caps map[string]bool
	if err := json.NewDecoder(resp.Body).Decode(&caps); err != nil {
		t.Fatalf("failed to decode capabilities: %v", err)
	}
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

func TestHandleMessage_WhitespaceOnlyMessage(t *testing.T) {
	ctrl := &mockController{}
	srv := New("localhost:0", WithController(ctrl))
	body := bytes.NewBufferString(`{"message":"   "}`)
	req := httptest.NewRequest("POST", "/api/workitems/item-1/message", body)
	req.SetPathValue("itemID", "item-1")
	w := httptest.NewRecorder()
	srv.handleMessage(w, req)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace-only message, got %d", w.Result().StatusCode)
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

// ---- /api/auth ----

func TestHandleAuth_CachedInfo(t *testing.T) {
	ex := iexec.NewMockExecutor(nil)
	srv := New("localhost:0", WithAuthExecutor(ex))

	// Pre-populate a fresh cache — no subprocess should be invoked.
	srv.authCache = &AuthInfo{Email: "cached@example.com", IsLoggedIn: true}
	srv.authFetchAt = time.Now()

	req := httptest.NewRequest("GET", "/api/auth", nil)
	w := httptest.NewRecorder()
	srv.handleAuth(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var info AuthInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if info.Email != "cached@example.com" {
		t.Errorf("expected cached@example.com, got %q", info.Email)
	}

	if calls := ex.GetCalls(); len(calls) != 0 {
		t.Errorf("expected no subprocess calls, got %d", len(calls))
	}
}

func TestHandleAuth_FreshFetch(t *testing.T) {
	ex := iexec.NewMockExecutor(nil)
	ex.AddExactMatch("claude", []string{"auth", "status"}, iexec.MockResponse{
		Stdout: []byte(`{"isLoggedIn":true,"claudeAiOAuthAccount":{"emailAddress":"fresh@example.com"}}`),
	})

	srv := New("localhost:0", WithAuthExecutor(ex))
	// Cache is empty — should trigger fresh fetch.

	req := httptest.NewRequest("GET", "/api/auth", nil)
	w := httptest.NewRecorder()
	srv.handleAuth(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var info AuthInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if info.Email != "fresh@example.com" {
		t.Errorf("expected fresh@example.com, got %q", info.Email)
	}

	if calls := ex.GetCalls(); len(calls) != 1 {
		t.Errorf("expected 1 subprocess call, got %d", len(calls))
	}
}

func TestHandleAuth_StaleCache(t *testing.T) {
	ex := iexec.NewMockExecutor(nil)
	ex.AddExactMatch("claude", []string{"auth", "status"}, iexec.MockResponse{
		Stdout: []byte(`{"isLoggedIn":true,"claudeAiOAuthAccount":{"emailAddress":"refreshed@example.com"}}`),
	})

	srv := New("localhost:0", WithAuthExecutor(ex))
	// Set a stale cache (older than 5 minutes).
	srv.authCache = &AuthInfo{Email: "stale@example.com", IsLoggedIn: true}
	srv.authFetchAt = time.Now().Add(-10 * time.Minute)

	req := httptest.NewRequest("GET", "/api/auth", nil)
	w := httptest.NewRecorder()
	srv.handleAuth(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var info AuthInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if info.Email != "refreshed@example.com" {
		t.Errorf("expected refreshed@example.com, got %q", info.Email)
	}
}

func TestHandleAuth_CommandFails(t *testing.T) {
	ex := iexec.NewMockExecutor(nil)
	ex.AddExactMatch("claude", []string{"auth", "status"}, iexec.MockResponse{
		Err: fmt.Errorf("claude not found"),
	})

	srv := New("localhost:0", WithAuthExecutor(ex))
	req := httptest.NewRequest("GET", "/api/auth", nil)
	w := httptest.NewRecorder()
	srv.handleAuth(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Should return empty object when subprocess fails.
	body := strings.TrimSpace(w.Body.String())
	if body != "{}" {
		t.Errorf("expected empty object {}, got %q", body)
	}
}

func TestHandleAuth_FetchErrorRateLimited(t *testing.T) {
	ex := iexec.NewMockExecutor(nil)
	ex.AddExactMatch("claude", []string{"auth", "status"}, iexec.MockResponse{
		Err: fmt.Errorf("claude not found"),
	})

	srv := New("localhost:0", WithAuthExecutor(ex))
	// Zero authFetchAt so cache is considered absent/stale.

	req := httptest.NewRequest("GET", "/api/auth", nil)
	w := httptest.NewRecorder()
	srv.handleAuth(w, req)

	// authFetchAt must be updated even though the fetch failed.
	srv.authMu.Lock()
	fetchAt := srv.authFetchAt
	srv.authMu.Unlock()
	if fetchAt.IsZero() {
		t.Error("expected authFetchAt to be set even after a failed fetch")
	}

	// A second request within the TTL must not trigger another subprocess call.
	req2 := httptest.NewRequest("GET", "/api/auth", nil)
	w2 := httptest.NewRecorder()
	srv.handleAuth(w2, req2)

	if calls := ex.GetCalls(); len(calls) != 1 {
		t.Errorf("expected exactly 1 subprocess call across both requests, got %d", len(calls))
	}
}

func TestHandleAuth_StaleCacheReturnedOnError(t *testing.T) {
	ex := iexec.NewMockExecutor(nil)
	ex.AddExactMatch("claude", []string{"auth", "status"}, iexec.MockResponse{
		Err: fmt.Errorf("claude not found"),
	})

	srv := New("localhost:0", WithAuthExecutor(ex))
	// Pre-populate a stale cache so we can verify it is returned on fetch error.
	srv.authCache = &AuthInfo{Email: "stale@example.com", IsLoggedIn: true}
	srv.authFetchAt = time.Now().Add(-10 * time.Minute)

	req := httptest.NewRequest("GET", "/api/auth", nil)
	w := httptest.NewRecorder()
	srv.handleAuth(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var info AuthInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	// The stale cache value should be returned, not an empty object.
	if info.Email != "stale@example.com" {
		t.Errorf("expected stale@example.com from cache, got %q", info.Email)
	}
}

// ---- buildOrigin ----

func TestBuildOrigin(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{"localhost", "localhost:8080", "http://localhost:8080"},
		{"127.0.0.1", "127.0.0.1:21122", "http://127.0.0.1:21122"},
		{"::1", "[::1]:8080", "http://[::1]:8080"},
		{"empty host", ":8080", "http://localhost:8080"},
		{"0.0.0.0", "0.0.0.0:8080", "http://localhost:8080"},
		{"::", "[::]:8080", "http://localhost:8080"},
		{"invalid", "notanaddr", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildOrigin(tt.addr)
			if got != tt.want {
				t.Errorf("buildOrigin(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// ---- corsMiddleware ----

func TestCORSMiddleware_NoOrigin(t *testing.T) {
	allowedOrigin := "http://localhost:21122"
	handler := corsMiddleware(allowedOrigin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200 for no-origin request, got %d", w.Result().StatusCode)
	}
}

func TestCORSMiddleware_MatchingOrigin(t *testing.T) {
	allowedOrigin := "http://localhost:21122"
	handler := corsMiddleware(allowedOrigin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for matching origin, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("expected ACAO=%q, got %q", allowedOrigin, got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "false" {
		t.Errorf("expected ACAC=false, got %q", got)
	}
}

func TestCORSMiddleware_NonMatchingOrigin(t *testing.T) {
	allowedOrigin := "http://localhost:21122"
	handler := corsMiddleware(allowedOrigin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for non-matching origin, got %d", w.Result().StatusCode)
	}
}

func TestCORSMiddleware_OptionsPreflightMatchingOrigin(t *testing.T) {
	allowedOrigin := "http://localhost:21122"
	handler := corsMiddleware(allowedOrigin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/api/state", nil)
	req.Header.Set("Origin", allowedOrigin)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS preflight with matching origin, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != allowedOrigin {
		t.Errorf("expected ACAO=%q, got %q", allowedOrigin, got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("expected Access-Control-Allow-Methods to be set")
	}
}

func TestCORSMiddleware_OptionsPreflightNonMatchingOrigin(t *testing.T) {
	allowedOrigin := "http://localhost:21122"
	handler := corsMiddleware(allowedOrigin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("OPTIONS", "/api/state", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for OPTIONS preflight with non-matching origin, got %d", w.Result().StatusCode)
	}
}

// ---- validateLoopback ----

func TestValidateLoopback(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"localhost", "localhost:8080", false},
		{"127.0.0.1", "127.0.0.1:8080", false},
		{"::1", "[::1]:8080", false},
		{"empty host", ":8080", false},
		{"0.0.0.0 rejected", "0.0.0.0:8080", true},
		{"public IP rejected", "192.168.1.1:8080", true},
		{"no port", "localhost", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLoopback(tt.addr)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLoopback(%q) error = %v, wantErr %v", tt.addr, err, tt.wantErr)
			}
		})
	}
}

func TestRun_RejectsNonLoopbackWithController(t *testing.T) {
	ctrl := &mockController{}
	srv := New("0.0.0.0:0", WithController(ctrl))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := srv.Run(ctx)
	if err == nil {
		t.Fatal("expected error for non-loopback address with controller")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("expected loopback error, got: %v", err)
	}
}

func TestRun_AllowsNonLoopbackWithoutController(t *testing.T) {
	// Without a controller, any address should be allowed (read-only mode).
	srv := New("0.0.0.0:0")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so Run exits quickly
	// Run should not fail due to loopback validation; it may fail for other
	// reasons (context cancelled) but not with a loopback error.
	err := srv.Run(ctx)
	if err != nil && strings.Contains(err.Error(), "loopback") {
		t.Errorf("unexpected loopback error without controller: %v", err)
	}
}
