package workflow

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const workflowFileName = "workflow.yaml"
const workflowDir = ".erg"

// Load reads and parses .erg/workflow.yaml from the given repo path.
// Returns nil, nil if the file does not exist.
func Load(repoPath string) (*Config, error) {
	fp := filepath.Join(repoPath, workflowDir, workflowFileName)

	data, err := os.ReadFile(fp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read workflow config: %w", err)
	}

	// Detect old format by checking for "workflow:" key with nested struct (coding/pr/review etc.)
	if isOldFormat(data) {
		return nil, fmt.Errorf(
			"workflow config uses the old flat format which is no longer supported. " +
				"Please migrate to the new step-functions format. " +
				"Run `erg workflow init` to see the new format, " +
				"or see https://github.com/zhubert/erg for migration docs",
		)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse workflow config: %w", err)
	}

	return &cfg, nil
}

// isOldFormat detects the old flat workflow config format.
// Old format has "workflow:" as a map with keys like "coding", "pr", "review".
// New format has "states:" as a top-level key and "workflow:" is a string.
func isOldFormat(data []byte) bool {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}

	// The "states" key is the definitive marker for the new format.
	// A config with only "source" (no "states" and no "workflow" map) is
	// incomplete new-format, not old-format — it will fail validation later.
	if _, hasStates := raw["states"]; hasStates {
		return false
	}

	// If "workflow" is a map (not a string), it's the old format
	if wf, ok := raw["workflow"]; ok {
		if _, isMap := wf.(map[string]any); isMap {
			return true
		}
	}

	return false
}

// LoadFile reads and parses a workflow config from an explicit file path.
// Returns nil, nil if the file does not exist.
func LoadFile(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read workflow config: %w", err)
	}

	if isOldFormat(data) {
		return nil, fmt.Errorf(
			"workflow config uses the old flat format which is no longer supported. " +
				"Please migrate to the new step-functions format. " +
				"Run `erg workflow init` to see the new format, " +
				"or see https://github.com/zhubert/erg for migration docs",
		)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse workflow config: %w", err)
	}

	return &cfg, nil
}

// LoadAndMerge loads the workflow config and merges with defaults.
// If no workflow file exists, returns the default config.
func LoadAndMerge(repoPath string) (*Config, error) {
	return LoadAndMergeWithFile(repoPath, "")
}

// LoadAndMergeWithFile loads the workflow config and merges with defaults.
// If workflowFile is non-empty, it is used as the explicit path to the config
// file instead of the default <repoPath>/.erg/workflow.yaml.
func LoadAndMergeWithFile(repoPath, workflowFile string) (*Config, error) {
	var (
		cfg *Config
		err error
	)
	if workflowFile != "" {
		cfg, err = LoadFile(workflowFile)
	} else {
		cfg, err = Load(repoPath)
	}
	if err != nil {
		return nil, err
	}

	defaults := DefaultWorkflowConfig()
	if cfg == nil {
		return defaults, nil
	}

	// Expand template states before merging with defaults.
	cfg, err = ExpandTemplates(cfg, repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to expand workflow templates: %w", err)
	}

	return Merge(cfg, defaults), nil
}
