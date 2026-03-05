package manifest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Manifest defines a multi-repo configuration for a single erg daemon.
type Manifest struct {
	MaxConcurrent int         `yaml:"max_concurrent,omitempty"`
	Repos         []RepoEntry `yaml:"repos"`
}

// RepoEntry associates a repo with its workflow config file.
type RepoEntry struct {
	Repo     string `yaml:"repo"`
	Workflow string `yaml:"workflow,omitempty"`
}

// LoadFile reads and parses a manifest from the given file path.
func LoadFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	if len(m.Repos) == 0 {
		return nil, fmt.Errorf("manifest must contain at least one repo entry")
	}

	for i, entry := range m.Repos {
		if entry.Repo == "" {
			return nil, fmt.Errorf("manifest repos[%d]: repo is required", i)
		}
	}

	return &m, nil
}

// DaemonID returns a stable identifier for this manifest, derived from the
// sorted repo paths. This is used to key lock and state files so that the
// same set of repos always maps to the same daemon instance.
func (m *Manifest) DaemonID() string {
	repos := make([]string, len(m.Repos))
	for i, e := range m.Repos {
		repos[i] = e.Repo
	}
	sort.Strings(repos)
	h := sha256.New()
	for _, r := range repos {
		h.Write([]byte(r))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("multi-%x", h.Sum(nil)[:8])
}

// RepoPaths returns the list of repo paths from the manifest.
func (m *Manifest) RepoPaths() []string {
	paths := make([]string, len(m.Repos))
	for i, e := range m.Repos {
		paths[i] = e.Repo
	}
	return paths
}

// WorkflowFileFor returns the workflow file path for a given repo, or empty
// string if none was specified (meaning use the default <repo>/.erg/workflow.yaml).
func (m *Manifest) WorkflowFileFor(repo string) string {
	for _, e := range m.Repos {
		if e.Repo == repo {
			return e.Workflow
		}
	}
	return ""
}
