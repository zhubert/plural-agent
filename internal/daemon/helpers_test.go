package daemon

import (
	"testing"

	"github.com/zhubert/erg/internal/config"
	"github.com/zhubert/erg/internal/testutil"
)

func TestGetSessionOrError(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		setup     func(*config.Config)
		wantErr   bool
	}{
		{
			name:      "found",
			sessionID: "sess-1",
			setup: func(c *config.Config) {
				c.AddSession(config.Session{ID: "sess-1", RepoPath: "/repo"})
			},
			wantErr: false,
		},
		{
			name:      "not found",
			sessionID: "nonexistent",
			setup:     func(c *config.Config) {},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.TestConfig()
			tt.setup(cfg)

			d := &Daemon{config: cfg}
			sess, err := d.getSessionOrError(tt.sessionID)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if sess != nil {
					t.Fatal("expected nil session on error")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if sess == nil {
					t.Fatal("expected non-nil session")
				}
				if sess.ID != tt.sessionID {
					t.Errorf("session ID = %q, want %q", sess.ID, tt.sessionID)
				}
			}
		})
	}
}
