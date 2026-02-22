package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestExampleWorkflows loads every YAML file from the examples/ directory and
// checks that it parses and validates without errors. This keeps examples in
// sync with the workflow engine as the codebase evolves.
func TestExampleWorkflows(t *testing.T) {
	// internal/workflow is two levels below the repo root.
	examplesDir := filepath.Join("..", "..", "examples")

	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("failed to read examples directory %s: %v", examplesDir, err)
	}

	var yamlFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			yamlFiles = append(yamlFiles, e.Name())
		}
	}

	if len(yamlFiles) == 0 {
		t.Fatalf("no .yaml files found in %s", examplesDir)
	}

	for _, name := range yamlFiles {
		t.Run(name, func(t *testing.T) {
			fp := filepath.Join(examplesDir, name)

			data, err := os.ReadFile(fp)
			if err != nil {
				t.Fatalf("failed to read %s: %v", fp, err)
			}

			var cfg Config
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				t.Fatalf("failed to parse %s: %v", name, err)
			}

			errs := Validate(&cfg)
			if len(errs) > 0 {
				var msgs []string
				for _, e := range errs {
					msgs = append(msgs, fmt.Sprintf("  %s: %s", e.Field, e.Message))
				}
				t.Errorf("example %s has %d validation error(s):\n%s",
					name, len(errs), strings.Join(msgs, "\n"))
			}
		})
	}
}
