// Package container provides auto-detection of repository languages and
// automatic container image building for the erg agent.
package container

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Language represents a programming language detected in a repository.
type Language string

const (
	LangGo     Language = "go"
	LangNode   Language = "node"
	LangPython Language = "python"
	LangRuby   Language = "ruby"
	LangRust   Language = "rust"
	LangJava   Language = "java"
	LangPHP    Language = "php"
)

// DetectedLang pairs a language with its parsed version (may be empty).
type DetectedLang struct {
	Lang    Language
	Version string // e.g. "1.23", "20", "3.3.0" — empty means latest
}

// languageOrder defines a deterministic sort order for languages.
var languageOrder = map[Language]int{
	LangGo:     0,
	LangNode:   1,
	LangRuby:   2,
	LangPython: 3,
	LangRust:   4,
	LangJava:   5,
	LangPHP:    6,
}

// isLocalPath returns true if the repo string looks like a local filesystem path.
func isLocalPath(repo string) bool {
	return strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, ".")
}

// Detect detects languages used in the given repository.
// For local paths (starting with / or .), it checks for marker files on disk.
// For remote repos (owner/repo format), it uses the GitHub API.
func Detect(ctx context.Context, repoPath string) []DetectedLang {
	if isLocalPath(repoPath) {
		return detectLocal(repoPath)
	}
	return detectRemote(ctx, repoPath)
}

// markerFile maps a filename to the language it indicates.
type markerFile struct {
	file string
	lang Language
}

// markers is the list of files to check for language detection.
var markers = []markerFile{
	{"go.mod", LangGo},
	{"package.json", LangNode},
	{"Gemfile", LangRuby},
	{"requirements.txt", LangPython},
	{"pyproject.toml", LangPython},
	{"setup.py", LangPython},
	{"Cargo.toml", LangRust},
	{"pom.xml", LangJava},
	{"build.gradle", LangJava},
	{"build.gradle.kts", LangJava},
	{"composer.json", LangPHP},
}

// detectLocal checks for marker files on the local filesystem.
func detectLocal(repoPath string) []DetectedLang {
	seen := make(map[Language]bool)
	var result []DetectedLang

	for _, m := range markers {
		if seen[m.lang] {
			continue
		}
		path := filepath.Join(repoPath, m.file)
		if _, err := os.Stat(path); err == nil {
			seen[m.lang] = true
			version := parseVersion(repoPath, m.lang)
			result = append(result, DetectedLang{Lang: m.lang, Version: version})
		}
	}

	sortDetected(result)
	return result
}

// parseVersion attempts to parse the version for a language from repo files.
func parseVersion(repoPath string, lang Language) string {
	switch lang {
	case LangGo:
		return parseGoVersion(repoPath)
	case LangNode:
		return parseNodeVersion(repoPath)
	case LangRuby:
		return parseRubyVersion(repoPath)
	case LangPython:
		return parsePythonVersion(repoPath)
	case LangRust:
		return parseRustVersion(repoPath)
	case LangJava:
		return parseJavaVersion(repoPath)
	default:
		return ""
	}
}

var goVersionRe = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)`)

func parseGoVersion(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "go.mod"))
	if err != nil {
		return ""
	}
	m := goVersionRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func parseNodeVersion(repoPath string) string {
	// Priority: .node-version → .nvmrc → package.json engines.node
	if v := readTrimmedFile(filepath.Join(repoPath, ".node-version")); v != "" {
		return extractMajorVersion(v)
	}
	if v := readTrimmedFile(filepath.Join(repoPath, ".nvmrc")); v != "" {
		return extractMajorVersion(v)
	}
	return parseNodeFromPackageJSON(repoPath)
}

var nodeEngineRe = regexp.MustCompile(`(\d+)`)

func parseNodeFromPackageJSON(repoPath string) string {
	data, err := os.ReadFile(filepath.Join(repoPath, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Engines.Node == "" {
		return ""
	}
	// Extract the first number from the engines constraint (e.g., ">=20" → "20")
	m := nodeEngineRe.FindString(pkg.Engines.Node)
	return m
}

var rubyVersionRe = regexp.MustCompile(`(?m)ruby\s+["'](\d+\.\d+(?:\.\d+)?)["']`)

func parseRubyVersion(repoPath string) string {
	// Priority: .ruby-version → Gemfile ruby directive
	if v := readTrimmedFile(filepath.Join(repoPath, ".ruby-version")); v != "" {
		return strings.TrimPrefix(v, "ruby-")
	}
	data, err := os.ReadFile(filepath.Join(repoPath, "Gemfile"))
	if err != nil {
		return ""
	}
	m := rubyVersionRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

var pythonVersionRe = regexp.MustCompile(`(?m)requires-python\s*=\s*["']>=?(\d+\.\d+)`)

func parsePythonVersion(repoPath string) string {
	// Priority: .python-version → pyproject.toml requires-python
	if v := readTrimmedFile(filepath.Join(repoPath, ".python-version")); v != "" {
		return extractMajorMinorVersion(v)
	}
	data, err := os.ReadFile(filepath.Join(repoPath, "pyproject.toml"))
	if err != nil {
		return ""
	}
	m := pythonVersionRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

var rustToolchainRe = regexp.MustCompile(`(?m)channel\s*=\s*["'](\d+\.\d+(?:\.\d+)?)["']`)

func parseRustVersion(repoPath string) string {
	// Priority: rust-toolchain.toml → rust-toolchain
	data, err := os.ReadFile(filepath.Join(repoPath, "rust-toolchain.toml"))
	if err == nil {
		m := rustToolchainRe.FindSubmatch(data)
		if m != nil {
			return string(m[1])
		}
	}
	if v := readTrimmedFile(filepath.Join(repoPath, "rust-toolchain")); v != "" {
		return strings.TrimSpace(v)
	}
	return ""
}

func parseJavaVersion(repoPath string) string {
	if v := readTrimmedFile(filepath.Join(repoPath, ".java-version")); v != "" {
		return extractMajorVersion(v)
	}
	return ""
}

// readTrimmedFile reads a file and returns its trimmed contents, or "" on error.
func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Take only the first line
	s := strings.TrimSpace(string(data))
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

// extractMajorVersion extracts the leading major version number (e.g., "20.11.0" → "20").
func extractMajorVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// extractMajorMinorVersion extracts "X.Y" from a version string.
func extractMajorMinorVersion(v string) string {
	v = strings.TrimSpace(v)
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// ghLanguageMap maps GitHub API language names to our Language type.
var ghLanguageMap = map[string]Language{
	"Go":         LangGo,
	"JavaScript": LangNode,
	"TypeScript": LangNode,
	"Python":     LangPython,
	"Ruby":       LangRuby,
	"Rust":       LangRust,
	"Java":       LangJava,
	"Kotlin":     LangJava,
	"PHP":        LangPHP,
}

// ghCommandFunc is the function used to execute gh commands. Overridden in tests.
var ghCommandFunc = ghCommand

func ghCommand(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "gh", args...).Output()
}

// detectRemote uses the GitHub API to detect languages.
func detectRemote(ctx context.Context, repo string) []DetectedLang {
	out, err := ghCommandFunc(ctx, "api", fmt.Sprintf("repos/%s/languages", repo))
	if err != nil {
		return nil
	}

	var langs map[string]int64
	if err := json.Unmarshal(out, &langs); err != nil {
		return nil
	}

	seen := make(map[Language]bool)
	var result []DetectedLang

	for ghName := range langs {
		lang, ok := ghLanguageMap[ghName]
		if !ok || seen[lang] {
			continue
		}
		seen[lang] = true
		version := parseRemoteVersion(ctx, repo, lang)
		result = append(result, DetectedLang{Lang: lang, Version: version})
	}

	sortDetected(result)
	return result
}

// versionFiles maps languages to the files to try fetching for version detection.
var versionFiles = map[Language][]string{
	LangGo:     {"go.mod"},
	LangNode:   {".node-version", ".nvmrc", "package.json"},
	LangRuby:   {".ruby-version", "Gemfile"},
	LangPython: {".python-version", "pyproject.toml"},
	LangRust:   {"rust-toolchain.toml", "rust-toolchain"},
	LangJava:   {".java-version"},
}

// parseRemoteVersion fetches version files from a remote repo via the GitHub API.
func parseRemoteVersion(ctx context.Context, repo string, lang Language) string {
	files, ok := versionFiles[lang]
	if !ok {
		return ""
	}

	// Create a temp dir to store fetched files, then reuse local parsers
	tmpDir, err := os.MkdirTemp("", "erg-detect-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(tmpDir)

	for _, file := range files {
		content, err := fetchFileContent(ctx, repo, file)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(tmpDir, file)), 0o755); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(tmpDir, file), content, 0o644); err != nil {
			continue
		}
	}

	return parseVersion(tmpDir, lang)
}

// fetchFileContent fetches a file from a GitHub repo via the API and returns its decoded content.
func fetchFileContent(ctx context.Context, repo, path string) ([]byte, error) {
	out, err := ghCommandFunc(ctx, "api", fmt.Sprintf("repos/%s/contents/%s", repo, path))
	if err != nil {
		return nil, err
	}

	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	if resp.Encoding != "base64" {
		return nil, fmt.Errorf("unexpected encoding: %s", resp.Encoding)
	}

	return base64.StdEncoding.DecodeString(strings.ReplaceAll(resp.Content, "\n", ""))
}

// sortDetected sorts detected languages by the deterministic language order.
func sortDetected(langs []DetectedLang) {
	sort.Slice(langs, func(i, j int) bool {
		return languageOrder[langs[i].Lang] < languageOrder[langs[j].Lang]
	})
}
