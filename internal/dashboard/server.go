package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/zhubert/erg/internal/logger"
)

//go:embed index.html
var indexHTML embed.FS

// Server is the dashboard HTTP server with SSE support.
type Server struct {
	addr     string
	log      *slog.Logger
	pollRate time.Duration

	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

// New creates a new dashboard server.
func New(addr string) *Server {
	return &Server{
		addr:     addr,
		log:      logger.Get(),
		pollRate: 1500 * time.Millisecond,
		clients:  make(map[chan []byte]struct{}),
	}
}

// Run starts the HTTP server and background poller. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("GET /api/logs/{sessionID}", s.handleLogs)

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
		srv.Shutdown(context.Background())
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
	w.Header().Set("Access-Control-Allow-Origin", "*")

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
	tailN := 200
	if t := r.URL.Query().Get("tail"); t != "" {
		if n, err := strconv.Atoi(t); err == nil && n > 0 {
			tailN = n
		}
	}

	lines, err := ReadSessionLog(sessionID, tailN)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lines)
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
			if string(data) != string(lastJSON) {
				lastJSON = data
				s.broadcast(data)
			}
		}
	}
}
