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

// Server is the dashboard HTTP server with SSE support.
type Server struct {
	addr       string
	log        *slog.Logger
	pollRate   time.Duration
	controller SessionController // nil = read-only mode

	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

// New creates a new dashboard server.
func New(addr string, opts ...ServerOption) *Server {
	s := &Server{
		addr:     addr,
		log:      logger.Get(),
		pollRate: 1500 * time.Millisecond,
		clients:  make(map[chan []byte]struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run starts the HTTP server and background poller. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("GET /api/logs/{sessionID}", s.handleLogs)
	mux.HandleFunc("GET /api/capabilities", s.handleCapabilities)
	mux.HandleFunc("POST /api/workitems/{itemID}/stop", s.handleStop)
	mux.HandleFunc("POST /api/workitems/{itemID}/retry", s.handleRetry)
	mux.HandleFunc("POST /api/workitems/{itemID}/message", s.handleMessage)

	srv := &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	// Start background poller
	go s.poll(ctx)

	// Start server in goroutine
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
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

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.controller == nil {
		http.Error(w, "control not available", http.StatusServiceUnavailable)
		return
	}
	itemID := r.PathValue("itemID")
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
func (s *Server) poll(ctx context.Context) {
	ticker := time.NewTicker(s.pollRate)
	defer ticker.Stop()

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
		}
	}
}
