package daemon

import (
	"context"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"strings"

	"github.com/zhubert/erg/internal/daemonstate"
	"github.com/zhubert/erg/internal/workflow"
)

// defaultLockFilePatterns is the fallback set of lock file glob patterns used
// when the caller has not supplied a lock_file_patterns param.
var defaultLockFilePatterns = []string{
	"go.sum", "package-lock.json", "yarn.lock",
	"Pipfile.lock", "Gemfile.lock", "Cargo.lock",
}

// defaultSourcePatterns is the fallback set of source file glob patterns used
// when the caller has not supplied a source_patterns param.
var defaultSourcePatterns = []string{
	"*.go", "*.py", "*.js", "*.ts", "*.java",
	"*.rb", "*.rs", "*.cpp", "*.c", "*.cs",
}

// defaultTestPatterns is the fallback set of test file glob patterns used when
// the caller has not supplied a test_patterns param.
var defaultTestPatterns = []string{
	"*_test.go", "test_*.py", "*_test.py",
	"*.test.js", "*.spec.js", "*.test.ts", "*.spec.ts",
	"*Test.java", "*_spec.rb",
}

// validateDiff runs static diff validation checks on the committed changes
// between the work item's feature branch and its base branch.
//
// Supported params (all optional):
//
//   - max_diff_lines (int): Fail if total added+deleted lines exceed this value.
//   - forbidden_patterns ([]string): Fail if any changed file matches one of
//     these glob patterns (e.g. ".env", "*.pem").
//   - require_tests (bool): Fail if source files were modified but no test
//     files appear in the diff.
//   - source_patterns ([]string): Glob patterns that identify source files for
//     the require_tests check. Defaults to defaultSourcePatterns.
//   - test_patterns ([]string): Glob patterns that identify test files for the
//     require_tests check. Defaults to defaultTestPatterns.
//   - max_lock_file_lines (int): Fail if any single lock file has more than
//     this many lines changed.
//   - lock_file_patterns ([]string): Glob patterns for lock files. Defaults to
//     defaultLockFilePatterns.
//
// Returns a non-empty violations slice when checks fail, or an error if the
// checks could not be executed at all (e.g. git command failure).
func (d *Daemon) validateDiff(ctx context.Context, item daemonstate.WorkItem, params *workflow.ParamHelper) ([]string, error) {
	sess, err := d.getSessionOrError(item.SessionID)
	if err != nil {
		return nil, err
	}

	workDir := sess.GetWorkDir()

	baseBranch := sess.BaseBranch
	if baseBranch == "" {
		baseBranch = d.gitService.GetDefaultBranch(ctx, sess.RepoPath)
	}

	diffCtx, cancel := context.WithTimeout(ctx, timeoutGitPush)
	defer cancel()

	// Three-dot notation: diff from merge base of baseBranch and item.Branch
	// to item.Branch. This captures only commits introduced by the feature
	// branch, ignoring unrelated upstream changes.
	diffRef := baseBranch + "..." + item.Branch

	changedFiles, err := gitDiffNameOnly(diffCtx, workDir, diffRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get changed files: %w", err)
	}

	linesByFile, totalAdded, totalDeleted, err := gitDiffNumstat(diffCtx, workDir, diffRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get diff line stats: %w", err)
	}

	var violations []string

	// 1. Total diff size check.
	if maxLines := params.Int("max_diff_lines", 0); maxLines > 0 {
		total := totalAdded + totalDeleted
		if total > maxLines {
			violations = append(violations, fmt.Sprintf(
				"diff too large: %d lines changed (max %d)", total, maxLines))
		}
	}

	// 2. Forbidden file pattern check.
	if forbidden := paramStringSlice(params, "forbidden_patterns"); len(forbidden) > 0 {
		for _, f := range changedFiles {
			if matched, pattern := fileMatchesAny(f, forbidden); matched {
				violations = append(violations, fmt.Sprintf(
					"forbidden file in diff: %s (matched pattern %q)", f, pattern))
			}
		}
	}

	// 3. Lock file bloat check.
	if maxLockLines := params.Int("max_lock_file_lines", 0); maxLockLines > 0 {
		lockPatterns := paramStringSlice(params, "lock_file_patterns")
		if len(lockPatterns) == 0 {
			lockPatterns = defaultLockFilePatterns
		}
		for _, f := range changedFiles {
			if ok, _ := fileMatchesAny(f, lockPatterns); ok {
				if changed := linesByFile[f]; changed > maxLockLines {
					violations = append(violations, fmt.Sprintf(
						"lock file %s has too many changes: %d lines (max %d)", f, changed, maxLockLines))
				}
			}
		}
	}

	// 4. Require-tests check.
	if params.Bool("require_tests", false) {
		sourcePatterns := paramStringSlice(params, "source_patterns")
		if len(sourcePatterns) == 0 {
			sourcePatterns = defaultSourcePatterns
		}
		testPatterns := paramStringSlice(params, "test_patterns")
		if len(testPatterns) == 0 {
			testPatterns = defaultTestPatterns
		}

		hasSource := false
		hasTest := false
		for _, f := range changedFiles {
			if ok, _ := fileMatchesAny(f, testPatterns); ok {
				hasTest = true
			} else if ok, _ := fileMatchesAny(f, sourcePatterns); ok {
				hasSource = true
			}
		}
		if hasSource && !hasTest {
			violations = append(violations, "source files modified but no test files found in diff")
		}
	}

	return violations, nil
}

// gitDiffNameOnly returns the list of file paths changed between diffRef.
// An empty diff returns a nil slice (not an error).
func gitDiffNameOnly(ctx context.Context, workDir, diffRef string) ([]string, error) {
	cmd := osexec.CommandContext(ctx, "git", "diff", "--name-only", diffRef)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// gitDiffNumstat returns per-file line-change counts (additions + deletions)
// and overall totals for the given diffRef.
func gitDiffNumstat(ctx context.Context, workDir, diffRef string) (map[string]int, int, int, error) {
	cmd := osexec.CommandContext(ctx, "git", "diff", "--numstat", diffRef)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, 0, err
	}

	linesByFile := make(map[string]int)
	var totalAdded, totalDeleted int

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// numstat format: "<added>\t<deleted>\t<filename>"
		// Binary files show "-" for both counts.
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		var added, deleted int
		if parts[0] != "-" {
			fmt.Sscanf(parts[0], "%d", &added)
		}
		if parts[1] != "-" {
			fmt.Sscanf(parts[1], "%d", &deleted)
		}
		linesByFile[parts[2]] = added + deleted
		totalAdded += added
		totalDeleted += deleted
	}

	return linesByFile, totalAdded, totalDeleted, nil
}

// paramStringSlice extracts a []string value from a workflow param key.
// Handles both []string and []interface{} (the latter is common when params
// come from YAML/JSON unmarshaling).
func paramStringSlice(params *workflow.ParamHelper, key string) []string {
	raw := params.Raw(key)
	if raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// fileMatchesAny returns true (and the first matching pattern) if file matches
// any of the given glob patterns. Matching is attempted against both the full
// path and the base name so that patterns like "*.env" work for nested paths.
func fileMatchesAny(file string, patterns []string) (bool, string) {
	base := filepath.Base(file)
	for _, pattern := range patterns {
		if m, _ := filepath.Match(pattern, base); m {
			return true, pattern
		}
		if m, _ := filepath.Match(pattern, file); m {
			return true, pattern
		}
	}
	return false, ""
}
