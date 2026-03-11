package dashboard

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	iexec "github.com/zhubert/erg/internal/exec"
	"github.com/zhubert/erg/internal/logger"
)

//go:embed index.html
var indexHTML embed.FS

// SessionController allows the dashboard to issue commands to running sessions.
// No authentication is applied — ensure the server address is restricted to
// loopback (e.g., "localhost:port") to prevent remote access.
type SessionController interface {
	// StopSession cancels the running worker for the given work item ID.
	StopSession(itemID string) error
	// RetryWorkItem resets a failed/completed work item back to queued state.
	RetryWorkItem(itemID string) error
	// SendMessage injects a message into an active session's next turn.
	SendMessage(itemID, message string) error
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithController attaches a SessionController, enabling the control endpoints.
// When no controller is set the POST endpoints return 503.
func WithController(c SessionController) ServerOption {
	return func(s *Server) { s.controller = c }
}

// WithAuthExecutor sets a custom CommandExecutor for running `claude auth status`.
// Primarily used in tests to inject mock executors.
func WithAuthExecutor(e iexec.CommandExecutor) ServerOption {
	return func(s *Server) { s.authExec = e }
}

const authCacheTTL     = 5 * time.Minute
const authFetchTimeout = 10 * time.Second
const maxTailLines     = 10_000 // upper bound on ?tail= to limit memory usage

// Server is the dashboard HTTP server with SSE support.
type Server struct {
	addr       string
	log        *slog.Logger
	pollRate   time.Duration
	controller SessionController // nil = read-only mode
	authExec   iexec.CommandExecutor

	mu      sync.RWMutex
	clients map[chan []byte]struct{}

	authMu      sync.Mutex
	authCache   *AuthInfo
	authFetchAt time.Time
}

// New creates a new dashboard server.
func New(addr string, opts ...ServerOption) *Server {
	s := &Server{
		addr:     addr,
		log:      logger.Get(),
		pollRate: 1500 * time.Millisecond,
		clients:  make(map[chan []byte]struct{}),
		authExec: iexec.NewRealExecutor(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run starts the HTTP server and background poller. Blocks until ctx is cancelled.
// When a SessionController is attached, the address must resolve to a loopback
// interface to prevent remote access to the unauthenticated control endpoints.
func (s *Server) Run(ctx context.Context) error {
	// Enforce loopback-only when control endpoints are enabled.
	if s.controller != nil {
		if err := validateLoopback(s.addr); err != nil {
			return fmt.Errorf("dashboard with control enabled: %w", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("GET /api/logs/{sessionID}", s.handleLogs)
	mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
	mux.HandleFunc("GET /api/auth", s.handleAuth)
	mux.HandleFunc("POST /api/workitems/{itemID}/stop", s.handleStop)
	mux.HandleFunc("POST /api/workitems/{itemID}/retry", s.handleRetry)
	mux.HandleFunc("POST /api/workitems/{itemID}/message", s.handleMessage)

	// Start background poller
	go s.poll(ctx)

	// Start server in goroutine
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	// Build the allowed origin from the configured bind host (preserving
	// "localhost" when the user configured "localhost:PORT") combined with the
	// OS-assigned port from the listener.  Using ln.Addr().String() directly
	// would map "localhost" → "127.0.0.1" and cause the browser's
	// "Origin: http://localhost:PORT" to be rejected.
	configHost, _, _ := net.SplitHostPort(s.addr)
	_, resolvedPort, _ := net.SplitHostPort(ln.Addr().String())
	allowedOrigin := buildOrigin(net.JoinHostPort(configHost, resolvedPort))
	srv := &http.Server{
		Addr:    s.addr,
		Handler: corsMiddleware(allowedOrigin)(mux),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	s.log.Info("dashboard server started", "addr", ln.Addr().String())
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := indexHTML.ReadFile("index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap, err := CollectAll()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan []byte, 8)
	s.addClient(ch)
	defer s.removeClient(ch)

	// Send initial state immediately
	snap, err := CollectAll()
	if err == nil {
		data, _ := json.Marshal(snap)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\") || strings.Contains(sessionID, "..") {
		http.Error(w, "invalid session ID", http.StatusBadRequest)
		return
	}
	tailN := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			if n > maxTailLines {
				n = maxTailLines
			}
			tailN = n
		}
	}

	lines, err := ReadSessionLog(sessionID, tailN)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "log not found", http.StatusNotFound)
		} else {
			http.Error(w, "failed to read logs", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lines)
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"control": s.controller != nil})
}

// handleAuth returns cached auth info, fetching fresh data if the cache is
// absent or stale. When the subprocess fails, any previously-cached value is
// returned. If no value has been cached yet, an empty JSON object is returned.
// Either way the fetch attempt time is recorded so failures are rate-limited
// to authCacheTTL and do not hammer the system during outages.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	s.authMu.Lock()
	if time.Since(s.authFetchAt) > authCacheTTL {
		ctx, cancel := context.WithTimeout(r.Context(), authFetchTimeout)
		fresh, err := FetchAuthInfo(ctx, s.authExec)
		cancel()
		now := time.Now()
		if err == nil {
			s.authCache = fresh
		}
		s.authFetchAt = now
	}
	info := s.authCache
	s.authMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if info == nil {
		w.Write([]byte("{}\n"))
		return
	}
	json.NewEncoder(w).Encode(info)
}

// validateItemID rejects empty, path-separator-containing, or traversal item IDs
// consistent with the sessionID validation in handleLogs.
func validateItemID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/\\") && !strings.Contains(id, "..")
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "control not available", http.StatusServiceUnavailable)
		return
	}
	itemID := r.PathValue("itemID")
	if !validateItemID(itemID) {
		http.Error(w, "invalid item ID", http.StatusBadRequest)
		return
	}
	if err := s.controller.StopSession(itemID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "control not available", http.StatusServiceUnavailable)
		return
	}
	itemID := r.PathValue("itemID")
	if !validateItemID(itemID) {
		http.Error(w, "invalid item ID", http.StatusBadRequest)
		return
	}
	if err := s.controller.RetryWorkItem(itemID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// messageRequest is the body for the send-message endpoint.
type messageRequest struct {
	Message string `json:"message"`
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "control not available", http.StatusServiceUnavailable)
		return
	}
	itemID := r.PathValue("itemID")
	if !validateItemID(itemID) {
		http.Error(w, "invalid item ID", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	var req messageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid request: message field required", http.StatusBadRequest)
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		http.Error(w, "invalid request: message field required", http.StatusBadRequest)
		return
	}
	if err := s.controller.SendMessage(itemID, msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// buildOrigin converts a resolved TCP address (host:port) to an HTTP origin
// string. Wildcard/unspecified bind addresses (empty, 0.0.0.0, ::) are mapped
// to "localhost" so the origin matches what a browser sends.
func buildOrigin(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// corsMiddleware returns an HTTP middleware that blocks cross-origin requests.
// Requests with no Origin header (direct navigation, CLI tools) pass through
// unchanged. Requests whose Origin matches allowedOrigin pass through and
// receive the appropriate CORS response headers. All other origins receive
// 403 Forbidden, preventing malicious websites from making cross-origin
// requests to the dashboard.
func corsMiddleware(allowedOrigin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				// No Origin header — direct navigation, CLI tools, etc.
				next.ServeHTTP(w, r)
				return
			}
			if origin != allowedOrigin {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			// Origin matches — set CORS headers and handle preflight.
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// validateLoopback ensures the given address resolves to a loopback interface.
// This prevents accidentally exposing unauthenticated control endpoints to the
// network.
func validateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if host == "" {
		// An empty host causes net.Listen to bind on all interfaces (0.0.0.0),
		// not loopback. Require an explicit loopback address.
		return fmt.Errorf("address %q uses an empty host which binds to all interfaces; use localhost or 127.0.0.1", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Hostname — resolve it.
		ips, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("cannot resolve host %q: %w", host, err)
		}
		for _, resolved := range ips {
			if !resolved.IsLoopback() {
				return fmt.Errorf("address %q resolves to non-loopback IP %s; use localhost or 127.0.0.1", addr, resolved)
			}
		}
		return nil
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("address %q is not a loopback address; use localhost or 127.0.0.1", addr)
	}
	return nil
}

func (s *Server) addClient(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[ch] = struct{}{}
}

func (s *Server) removeClient(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, ch)
	close(ch)
}

func (s *Server) broadcast(data []byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ch := range s.clients {
		select {
		case ch <- data:
		default:
			// Client is slow, skip
		}
	}
}

// poll periodically collects state and broadcasts to SSE clients.
// It also refreshes the auth info cache every authCacheTTL.
func (s *Server) poll(ctx context.Context) {
	ticker := time.NewTicker(s.pollRate)
	defer ticker.Stop()

	authRefresh := time.NewTicker(authCacheTTL)
	defer authRefresh.Stop()

	var lastJSON []byte

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := CollectAll()
			if err != nil {
				continue
			}
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			// Only broadcast if state changed
			if !bytes.Equal(data, lastJSON) {
				lastJSON = data
				s.broadcast(data)
			}
		case <-authRefresh.C:
			authCtx, cancel := context.WithTimeout(ctx, authFetchTimeout)
			s.authMu.Lock()
			info, err := FetchAuthInfo(authCtx, s.authExec)
			if err == nil {
				s.authCache = info
				s.authFetchAt = time.Now()
			}
			s.authMu.Unlock()
			cancel()
		}
	}
}
