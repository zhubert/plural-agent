// Package testutil provides shared test helpers used across packages.
package testutil

import (
	"io"
	"log/slog"

	"github.com/zhubert/erg/internal/config"
)

// DiscardLogger returns a slog.Logger that discards all output.
func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestConfig returns a minimal config suitable for unit tests.
func TestConfig() *config.Config {
	return &config.Config{
		Repos:              []string{},
		Sessions:           []config.Session{},
		AllowedTools:       []string{},
		RepoAllowedTools:   make(map[string][]string),
		AutoMaxTurns:       50,
		AutoMaxDurationMin: 30,
	}
}
