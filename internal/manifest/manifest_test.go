package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile(t *testing.T) {
	t.Run("valid manifest", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "manifest.yaml")
		content := `
max_concurrent: 5
repos:
  - repo: owner/repo-a
    workflow: /path/to/a.yaml
  - repo: owner/repo-b
`
		os.WriteFile(fp, []byte(content), 0o644)

		m, err := LoadFile(fp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if m.MaxConcurrent != 5 {
			t.Errorf("expected max_concurrent=5, got %d", m.MaxConcurrent)
		}
		if len(m.Repos) != 2 {
			t.Fatalf("expected 2 repos, got %d", len(m.Repos))
		}
		if m.Repos[0].Repo != "owner/repo-a" {
			t.Errorf("expected owner/repo-a, got %s", m.Repos[0].Repo)
		}
		if m.Repos[0].Workflow != "/path/to/a.yaml" {
			t.Errorf("expected /path/to/a.yaml, got %s", m.Repos[0].Workflow)
		}
		if m.Repos[1].Workflow != "" {
			t.Errorf("expected empty workflow, got %s", m.Repos[1].Workflow)
		}
	})

	t.Run("empty repos", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "manifest.yaml")
		os.WriteFile(fp, []byte("repos: []\n"), 0o644)

		_, err := LoadFile(fp)
		if err == nil {
			t.Fatal("expected error for empty repos")
		}
	})

	t.Run("missing repo field", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "manifest.yaml")
		os.WriteFile(fp, []byte("repos:\n  - workflow: foo.yaml\n"), 0o644)

		_, err := LoadFile(fp)
		if err == nil {
			t.Fatal("expected error for missing repo")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := LoadFile("/nonexistent/manifest.yaml")
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "manifest.yaml")
		os.WriteFile(fp, []byte("{{invalid"), 0o644)

		_, err := LoadFile(fp)
		if err == nil {
			t.Fatal("expected error for invalid yaml")
		}
	})
}

func TestDaemonID(t *testing.T) {
	t.Run("stable across order", func(t *testing.T) {
		m1 := &Manifest{Repos: []RepoEntry{
			{Repo: "owner/a"},
			{Repo: "owner/b"},
		}}
		m2 := &Manifest{Repos: []RepoEntry{
			{Repo: "owner/b"},
			{Repo: "owner/a"},
		}}
		if m1.DaemonID() != m2.DaemonID() {
			t.Errorf("DaemonID should be order-independent: %s vs %s", m1.DaemonID(), m2.DaemonID())
		}
	})

	t.Run("different repos different ID", func(t *testing.T) {
		m1 := &Manifest{Repos: []RepoEntry{{Repo: "owner/a"}}}
		m2 := &Manifest{Repos: []RepoEntry{{Repo: "owner/b"}}}
		if m1.DaemonID() == m2.DaemonID() {
			t.Error("different repos should produce different DaemonIDs")
		}
	})

	t.Run("has multi prefix", func(t *testing.T) {
		m := &Manifest{Repos: []RepoEntry{{Repo: "owner/a"}}}
		id := m.DaemonID()
		if len(id) < 6 || id[:6] != "multi-" {
			t.Errorf("expected multi- prefix, got %s", id)
		}
	})
}

func TestRepoPaths(t *testing.T) {
	m := &Manifest{Repos: []RepoEntry{
		{Repo: "owner/a"},
		{Repo: "owner/b"},
	}}
	paths := m.RepoPaths()
	if len(paths) != 2 || paths[0] != "owner/a" || paths[1] != "owner/b" {
		t.Errorf("unexpected paths: %v", paths)
	}
}

func TestWorkflowFileFor(t *testing.T) {
	m := &Manifest{Repos: []RepoEntry{
		{Repo: "owner/a", Workflow: "/path/a.yaml"},
		{Repo: "owner/b"},
	}}
	if got := m.WorkflowFileFor("owner/a"); got != "/path/a.yaml" {
		t.Errorf("expected /path/a.yaml, got %s", got)
	}
	if got := m.WorkflowFileFor("owner/b"); got != "" {
		t.Errorf("expected empty, got %s", got)
	}
	if got := m.WorkflowFileFor("owner/c"); got != "" {
		t.Errorf("expected empty for unknown repo, got %s", got)
	}
}
