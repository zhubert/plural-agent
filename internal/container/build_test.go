package container

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateDockerfile_GoWithVersion(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangGo, Version: "1.23"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	expectedArch := goArch()
	expected := fmt.Sprintf("go1.23.0.linux-%s.tar.gz", expectedArch)
	if !strings.Contains(df, expected) {
		t.Errorf("expected %s in Dockerfile, got:\n%s", expected, df)
	}
	if !strings.Contains(df, "/usr/local/go/bin") {
		t.Error("expected Go PATH setup in Dockerfile")
	}
}

func TestGenerateDockerfile_MultiLanguage(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangGo, Version: "1.22"},
		{Lang: LangRuby, Version: "3.3"},
		{Lang: LangNode, Version: "20"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("go1.22.0.linux-%s.tar.gz", goArch())
	if !strings.Contains(df, expected) {
		t.Errorf("expected %s in Dockerfile", expected)
	}
	if !strings.Contains(df, "mise install ruby@3.3") {
		t.Error("expected mise install ruby 3.3 in Dockerfile")
	}
	if !strings.Contains(df, "node:20-alpine") {
		t.Error("expected Node 20 alpine base image")
	}
}

func TestGenerateDockerfile_NoLanguages(t *testing.T) {
	df, err := GenerateDockerfile(nil, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "node:20-alpine") {
		t.Error("expected alpine base image with default Node 20")
	}
	if !strings.Contains(df, "claude-code") {
		t.Error("expected Claude Code install")
	}
}

func TestGenerateDockerfile_AlwaysIncludesClaudeCode(t *testing.T) {
	tests := []struct {
		name  string
		langs []DetectedLang
	}{
		{"no languages", nil},
		{"go only", []DetectedLang{{Lang: LangGo, Version: "1.23"}}},
		{"multi", []DetectedLang{{Lang: LangGo}, {Lang: LangRuby}, {Lang: LangPython}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df, err := GenerateDockerfile(tt.langs, "0.2.11", "")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(df, "@anthropic-ai/claude-code") {
				t.Error("expected Claude Code install in every Dockerfile")
			}
		})
	}
}

func TestGenerateDockerfile_AlwaysIncludesEntrypoint(t *testing.T) {
	tests := []struct {
		name  string
		langs []DetectedLang
	}{
		{"no languages", nil},
		{"go only", []DetectedLang{{Lang: LangGo, Version: "1.23"}}},
		{"multi", []DetectedLang{{Lang: LangGo}, {Lang: LangRuby}, {Lang: LangPython}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df, err := GenerateDockerfile(tt.langs, "0.2.11", "")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(df, "entrypoint.sh") {
				t.Error("expected entrypoint script in every Dockerfile")
			}
			if !strings.Contains(df, "npm install -g @anthropic-ai/claude-code@latest") {
				t.Error("expected Claude Code install @latest in entrypoint script")
			}
			if !strings.Contains(df, "ERG_SKIP_UPDATE") {
				t.Error("expected ERG_SKIP_UPDATE check in entrypoint script")
			}
			if !strings.Contains(df, `exec claude`) {
				t.Error("expected exec claude in entrypoint script")
			}
			if !strings.Contains(df, `ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]`) {
				t.Error("expected ENTRYPOINT for entrypoint script in every Dockerfile")
			}
		})
	}
}

func TestGenerateDockerfile_EmptyVersionUsesDefault(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangGo, Version: ""},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	// Should use default Go version 1.23
	expected := fmt.Sprintf("go1.23.0.linux-%s.tar.gz", goArch())
	if !strings.Contains(df, expected) {
		t.Errorf("expected %s when version is empty", expected)
	}
}

func TestGenerateDockerfile_PythonWithVersion(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangPython, Version: "3.11"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "mise install python@3.11") {
		t.Error("expected mise install python@3.11 in Dockerfile")
	}
	if !strings.Contains(df, "mise use -g python@3.11") {
		t.Error("expected mise use -g python@3.11 in Dockerfile")
	}
}

func TestGenerateDockerfile_RustWithVersion(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangRust, Version: "1.77.0"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "--default-toolchain 1.77.0") {
		t.Error("expected Rust 1.77.0 toolchain in Dockerfile")
	}
	if !strings.Contains(df, "rustup.rs") {
		t.Error("expected rustup install in Dockerfile")
	}
}

func TestGenerateDockerfile_JavaWithVersion(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangJava, Version: "21"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "openjdk21-jdk") {
		t.Error("expected OpenJDK 21 JDK install in Dockerfile")
	}
}

func TestGenerateDockerfile_PHP(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangPHP},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "php83-cli") {
		t.Error("expected php83-cli install in Dockerfile")
	}
	if !strings.Contains(df, "php83-phar") {
		t.Error("expected php83-phar install in Dockerfile")
	}
	if !strings.Contains(df, "ln -s /usr/bin/php83 /usr/bin/php") {
		t.Error("expected php symlink in Dockerfile")
	}
	if !strings.Contains(df, "composer") {
		t.Error("expected Composer install in Dockerfile")
	}
}

func TestGenerateDockerfile_IncludesErgBinary(t *testing.T) {
	expectedArch := releaseArch()
	tests := []struct {
		name  string
		langs []DetectedLang
	}{
		{"no languages", nil},
		{"go only", []DetectedLang{{Lang: LangGo, Version: "1.23"}}},
		{"multi", []DetectedLang{{Lang: LangGo}, {Lang: LangRuby}, {Lang: LangPython}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df, err := GenerateDockerfile(tt.langs, "0.2.11", "")
			if err != nil {
				t.Fatal(err)
			}
			expected := fmt.Sprintf("erg/releases/download/v0.2.11/erg_Linux_%s.tar.gz", expectedArch)
			if !strings.Contains(df, expected) {
				t.Errorf("expected release download URL in Dockerfile, got:\n%s", df)
			}
			if !strings.Contains(df, "/usr/local/bin/erg") {
				t.Error("expected erg binary install path in Dockerfile")
			}
		})
	}
}

func TestGenerateDockerfile_DevVersionUsesLatestRelease(t *testing.T) {
	for _, version := range []string{"dev", ""} {
		t.Run("version="+version, func(t *testing.T) {
			df, err := GenerateDockerfile(nil, version, "")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(df, "/releases/download/v") {
				t.Error("dev version should not use pinned release URL")
			}
			if !strings.Contains(df, "/releases/latest/download/") {
				t.Error("dev version should use latest release URL")
			}
			if !strings.Contains(df, "/usr/local/bin/erg") {
				t.Error("dev version should still install erg binary")
			}
		})
	}
}

func TestGenerateDockerfile_Deterministic(t *testing.T) {
	langs := []DetectedLang{
		{Lang: LangGo, Version: "1.23"},
		{Lang: LangNode, Version: "20"},
		{Lang: LangRuby, Version: "3.3"},
	}
	df1, err := GenerateDockerfile(langs, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	df2, err := GenerateDockerfile(langs, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if df1 != df2 {
		t.Error("GenerateDockerfile should be deterministic for the same input")
	}
}

func TestGenerateDockerfile_NodeVersionOverride(t *testing.T) {
	// When Node is detected with a specific version, it should use that version
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangNode, Version: "22"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "node:22-alpine") {
		t.Error("expected detected Node version 22 to override default in base image")
	}
	if strings.Contains(df, "node:20-alpine") {
		t.Error("should not use default Node 20 when version 22 is detected")
	}
}

func TestReleaseArch(t *testing.T) {
	arch := releaseArch()
	switch runtime.GOARCH {
	case "arm64":
		if arch != "arm64" {
			t.Errorf("expected arm64, got %s", arch)
		}
	default:
		if arch != "x86_64" {
			t.Errorf("expected x86_64, got %s", arch)
		}
	}
}

func TestImageTag_Deterministic(t *testing.T) {
	df := "FROM ubuntu:24.04\nRUN echo hello\n"
	tag1 := ImageTag(df)
	tag2 := ImageTag(df)
	if tag1 != tag2 {
		t.Errorf("ImageTag should be deterministic: %q != %q", tag1, tag2)
	}
}

func TestImageTag_DifferentContent(t *testing.T) {
	tag1 := ImageTag("FROM ubuntu:24.04\nRUN echo hello\n")
	tag2 := ImageTag("FROM ubuntu:24.04\nRUN echo world\n")
	if tag1 == tag2 {
		t.Error("ImageTag should differ for different content")
	}
}

func TestImageTag_Format(t *testing.T) {
	tag := ImageTag("test content")
	if !strings.HasPrefix(tag, "erg:") {
		t.Errorf("expected tag to start with 'erg:', got %q", tag)
	}
	// 12 hex chars from 6 bytes
	parts := strings.SplitN(tag, ":", 2)
	if len(parts[1]) != 12 {
		t.Errorf("expected 12 hex char hash, got %d chars: %q", len(parts[1]), parts[1])
	}
}

func TestEnsureImage_Cached(t *testing.T) {
	orig := dockerCommandFunc
	defer func() { dockerCommandFunc = orig }()

	inspectCalled := false
	dockerCommandFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			inspectCalled = true
			return []byte("exists"), nil // Image exists
		}
		t.Error("should not call docker build when image is cached")
		return nil, fmt.Errorf("unexpected call")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	tag, built, err := EnsureImage(context.Background(), []DetectedLang{{Lang: LangGo, Version: "1.23"}}, "0.2.11", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inspectCalled {
		t.Error("expected docker image inspect to be called")
	}
	if built {
		t.Error("expected built to be false when image is cached")
	}
	if !strings.HasPrefix(tag, "erg:") {
		t.Errorf("expected tag to start with 'erg:', got %q", tag)
	}
}

func TestEnsureImage_BuildsWhenNotCached(t *testing.T) {
	orig := dockerCommandFunc
	defer func() { dockerCommandFunc = orig }()

	buildCalled := false
	var buildContextArg string
	dockerCommandFunc = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			return nil, fmt.Errorf("not found") // Not cached
		}
		if args[0] == "build" {
			buildCalled = true
			if stdin == "" {
				t.Error("expected Dockerfile content on stdin")
			}
			if args[1] != "-t" {
				t.Error("expected -t flag")
			}
			buildContextArg = args[len(args)-1]
			return []byte("built"), nil
		}
		return nil, fmt.Errorf("unexpected call: %v", args)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	tag, built, err := EnsureImage(context.Background(), []DetectedLang{{Lang: LangGo, Version: "1.23"}}, "0.2.11", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !buildCalled {
		t.Error("expected docker build to be called")
	}
	if !built {
		t.Error("expected built to be true when image was not cached")
	}
	if !strings.HasPrefix(tag, "erg:") {
		t.Errorf("expected valid tag, got %q", tag)
	}
	// Non-dev builds should use an empty temp dir, not "." as build context
	if buildContextArg == "." {
		t.Error("expected build context to be a temp dir, not '.' (avoids scanning large/inaccessible directories)")
	}
}

func TestEnsureImage_BuildFailure(t *testing.T) {
	orig := dockerCommandFunc
	defer func() { dockerCommandFunc = orig }()

	dockerCommandFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			return nil, fmt.Errorf("not found")
		}
		return nil, fmt.Errorf("build error: out of disk space")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, _, err := EnsureImage(context.Background(), nil, "0.2.11", logger)
	if err == nil {
		t.Fatal("expected error on build failure")
	}
	if !strings.Contains(err.Error(), "docker build failed") {
		t.Errorf("expected 'docker build failed' in error, got: %v", err)
	}
}

func TestEnsureImage_DevUsesCaching(t *testing.T) {
	origDocker := dockerCommandFunc
	origCross := crossCompileFunc
	defer func() {
		dockerCommandFunc = origDocker
		crossCompileFunc = origCross
	}()

	// Simulate cross-compile writing a binary
	crossCompileFunc = func(outputPath string) error {
		return os.WriteFile(outputPath, []byte("fake-erg-binary"), 0o755)
	}

	inspectCalled := false
	dockerCommandFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			inspectCalled = true
			return []byte("exists"), nil
		}
		t.Error("should not call docker build when image is cached")
		return nil, fmt.Errorf("unexpected call")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, _, err := EnsureImage(context.Background(), nil, "dev", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inspectCalled {
		t.Error("dev builds should still check image cache")
	}
}

func TestGenerateDockerfile_DevBinaryHash(t *testing.T) {
	hash := "abc123def456"
	df, err := GenerateDockerfile(nil, "dev", hash)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(df, "COPY erg /usr/local/bin/erg") {
		t.Error("dev build with hash should use COPY instead of curl")
	}
	if !strings.Contains(df, "LABEL erg.dev.hash="+hash) {
		t.Error("dev build with hash should include hash label")
	}
	if strings.Contains(df, "curl") && strings.Contains(df, "erg_Linux") {
		t.Error("dev build with hash should not download erg from releases")
	}
}

func TestGenerateDockerfile_DevBinaryHashChangesTag(t *testing.T) {
	df1, err := GenerateDockerfile(nil, "dev", "hash_aaa")
	if err != nil {
		t.Fatal(err)
	}
	df2, err := GenerateDockerfile(nil, "dev", "hash_bbb")
	if err != nil {
		t.Fatal(err)
	}
	tag1 := ImageTag(df1)
	tag2 := ImageTag(df2)
	if tag1 == tag2 {
		t.Error("different binary hashes should produce different image tags")
	}
}

func TestGenerateDockerfile_DevNoHashFallsBackToLatest(t *testing.T) {
	df, err := GenerateDockerfile(nil, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(df, "COPY erg") {
		t.Error("dev build without hash should not use COPY")
	}
	if !strings.Contains(df, "/releases/latest/download/") {
		t.Error("dev build without hash should fall back to latest release")
	}
}

func TestEnsureImage_DevCrossCompiles(t *testing.T) {
	origDocker := dockerCommandFunc
	origCross := crossCompileFunc
	defer func() {
		dockerCommandFunc = origDocker
		crossCompileFunc = origCross
	}()

	crossCompileCalled := false
	crossCompileFunc = func(outputPath string) error {
		crossCompileCalled = true
		return os.WriteFile(outputPath, []byte("fake-erg-binary"), 0o755)
	}

	var buildStdin string
	var buildArgs []string
	dockerCommandFunc = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			return nil, fmt.Errorf("not found")
		}
		if args[0] == "build" {
			buildStdin = stdin
			buildArgs = args
			return []byte("built"), nil
		}
		return nil, fmt.Errorf("unexpected: %v", args)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	tag, built, err := EnsureImage(context.Background(), nil, "dev", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !crossCompileCalled {
		t.Error("expected cross-compile to be called for dev builds")
	}
	if !built {
		t.Error("expected image to be built")
	}
	if !strings.HasPrefix(tag, "erg:") {
		t.Errorf("expected valid tag, got %q", tag)
	}
	if !strings.Contains(buildStdin, "COPY erg /usr/local/bin/erg") {
		t.Error("expected COPY erg in Dockerfile for dev build")
	}
	if !strings.Contains(buildStdin, "LABEL erg.dev.hash=") {
		t.Error("expected hash label in Dockerfile for dev build")
	}
	// Build context should be a temp dir, not "."
	lastArg := buildArgs[len(buildArgs)-1]
	if lastArg == "." {
		t.Error("expected build context to be temp dir, not '.'")
	}
}

func TestEnsureImage_DevCrossCompileFailureFallsBack(t *testing.T) {
	origDocker := dockerCommandFunc
	origCross := crossCompileFunc
	defer func() {
		dockerCommandFunc = origDocker
		crossCompileFunc = origCross
	}()

	crossCompileFunc = func(_ string) error {
		return fmt.Errorf("go not found")
	}

	var buildStdin string
	dockerCommandFunc = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			return nil, fmt.Errorf("not found")
		}
		if args[0] == "build" {
			buildStdin = stdin
			return []byte("built"), nil
		}
		return nil, fmt.Errorf("unexpected: %v", args)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, built, err := EnsureImage(context.Background(), nil, "dev", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !built {
		t.Error("expected image to be built even on cross-compile failure")
	}
	// Should fall back to latest release download
	if strings.Contains(buildStdin, "COPY erg") {
		t.Error("should not use COPY when cross-compile fails")
	}
	if !strings.Contains(buildStdin, "/releases/latest/download/") {
		t.Error("should fall back to latest release download when cross-compile fails")
	}
}

func TestEnsureImage_ReleaseBuildSkipsCrossCompile(t *testing.T) {
	origDocker := dockerCommandFunc
	origCross := crossCompileFunc
	defer func() {
		dockerCommandFunc = origDocker
		crossCompileFunc = origCross
	}()

	crossCompileFunc = func(_ string) error {
		t.Error("cross-compile should not be called for release builds")
		return nil
	}

	dockerCommandFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "image" && args[1] == "inspect" {
			return nil, fmt.Errorf("not found")
		}
		return []byte("built"), nil
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	_, _, err := EnsureImage(context.Background(), nil, "0.2.11", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMiseInstallBlock_RubyAndPython(t *testing.T) {
	block, err := miseInstallBlock([]DetectedLang{
		{Lang: LangRuby, Version: "3.3"},
		{Lang: LangPython, Version: "3.12"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "mise install ruby@3.3") {
		t.Error("expected mise install ruby@3.3")
	}
	if !strings.Contains(block, "mise use -g ruby@3.3") {
		t.Error("expected mise use -g ruby@3.3")
	}
	if !strings.Contains(block, "mise install python@3.12") {
		t.Error("expected mise install python@3.12")
	}
	if !strings.Contains(block, "mise use -g python@3.12") {
		t.Error("expected mise use -g python@3.12")
	}
	if !strings.Contains(block, "mise.jdx.dev/install.sh") {
		t.Error("expected mise installer URL")
	}
}

func TestMiseInstallBlock_NoMiseLanguages(t *testing.T) {
	block, err := miseInstallBlock([]DetectedLang{
		{Lang: LangGo, Version: "1.23"},
		{Lang: LangNode, Version: "20"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if block != "" {
		t.Errorf("expected empty block for non-mise languages, got: %s", block)
	}
}

func TestMiseInstallBlock_DefaultVersion(t *testing.T) {
	block, err := miseInstallBlock([]DetectedLang{
		{Lang: LangRuby, Version: ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "mise install ruby@3.3") {
		t.Errorf("expected default Ruby version 3.3, got: %s", block)
	}
}

func TestMiseInstallBlock_Empty(t *testing.T) {
	block, err := miseInstallBlock(nil)
	if err != nil {
		t.Fatal(err)
	}
	if block != "" {
		t.Errorf("expected empty block for nil langs, got: %s", block)
	}
}

func TestGenerateDockerfile_MiseShimsInPath(t *testing.T) {
	df, err := GenerateDockerfile([]DetectedLang{
		{Lang: LangRuby, Version: "3.3"},
	}, "0.2.11", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "/root/.local/share/mise/shims") {
		t.Error("expected mise shims in PATH")
	}
	if !strings.Contains(df, "/root/.local/bin") {
		t.Error("expected mise bin dir in PATH")
	}
}

func TestIsValidVersion(t *testing.T) {
	tests := []struct {
		version string
		valid   bool
	}{
		// Valid versions
		{"20", true},
		{"1.23", true},
		{"3.3.0", true},
		{"21", true},
		{"1.77", true},
		{"1.77.0", true},
		{"3.12", true},
		// Invalid versions
		{"", false},
		{"1.23 && echo hi", false},
		{"1.2.3 ; rm -rf /", false},
		{"abc", false},
		{"1.", false},
		{".23", false},
		{"ruby-3.3", false},
		{">=3.0", false},
		{"1.2.3-beta", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := isValidVersion(tt.version)
			if got != tt.valid {
				t.Errorf("isValidVersion(%q) = %v, want %v", tt.version, got, tt.valid)
			}
		})
	}
}

func TestIsValidRustVersion(t *testing.T) {
	tests := []struct {
		version string
		valid   bool
	}{
		// Numeric releases
		{"1.77", true},
		{"1.77.0", true},
		{"1.23", true},
		// Named channels
		{"stable", true},
		{"beta", true},
		{"nightly", true},
		// Date-pinned channels
		{"nightly-2024-01-01", true},
		{"beta-2024-01-01", true},
		// Invalid
		{"", false},
		{"stable && echo hi", false},
		{"nightly; rm -rf /", false},
		{"nightly-latest", false},
		{"1.77 && evil", false},
		{"abc", false},
		{"1.", false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := isValidRustVersion(tt.version)
			if got != tt.valid {
				t.Errorf("isValidRustVersion(%q) = %v, want %v", tt.version, got, tt.valid)
			}
		})
	}
}

func TestGenerateDockerfile_RustChannelVersions(t *testing.T) {
	tests := []struct {
		name    string
		version string
	}{
		{"stable channel", "stable"},
		{"beta channel", "beta"},
		{"nightly channel", "nightly"},
		{"date-pinned nightly", "nightly-2024-01-01"},
		{"date-pinned beta", "beta-2024-01-01"},
		{"numeric version", "1.77.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df, err := GenerateDockerfile([]DetectedLang{
				{Lang: LangRust, Version: tt.version},
			}, "0.2.11", "")
			if err != nil {
				t.Fatalf("unexpected error for Rust version %q: %v", tt.version, err)
			}
			if !strings.Contains(df, "--default-toolchain "+tt.version) {
				t.Errorf("expected --default-toolchain %s in Dockerfile", tt.version)
			}
		})
	}
}

func TestGenerateDockerfile_RejectsInvalidVersion(t *testing.T) {
	tests := []struct {
		name  string
		langs []DetectedLang
	}{
		{
			"go with injection",
			[]DetectedLang{{Lang: LangGo, Version: "1.23 && malicious_command"}},
		},
		{
			"rust with injection",
			[]DetectedLang{{Lang: LangRust, Version: "1.77 && malicious_command"}},
		},
		{
			"ruby with injection",
			[]DetectedLang{{Lang: LangRuby, Version: "3.3 ; rm -rf /"}},
		},
		{
			"python with injection",
			[]DetectedLang{{Lang: LangPython, Version: "3.12 && echo hi"}},
		},
		{
			"node with injection",
			[]DetectedLang{{Lang: LangNode, Version: "20 && evil"}},
		},
		{
			"java with injection",
			[]DetectedLang{{Lang: LangJava, Version: "21 && evil"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := GenerateDockerfile(tt.langs, "0.2.11", "")
			if err == nil {
				t.Error("expected error for invalid version string, got nil")
			}
		})
	}
}

func TestLanguageInstallBlock_RejectsInvalidVersion(t *testing.T) {
	tests := []struct {
		name string
		lang DetectedLang
	}{
		{"go injection", DetectedLang{Lang: LangGo, Version: "1.23 && evil"}},
		{"rust injection", DetectedLang{Lang: LangRust, Version: "1.77 && evil"}},
		{"ruby injection", DetectedLang{Lang: LangRuby, Version: "3.3 ; rm -rf /"}},
		{"python injection", DetectedLang{Lang: LangPython, Version: "3.12 && echo hi"}},
		{"java injection", DetectedLang{Lang: LangJava, Version: "21 && evil"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := languageInstallBlock(tt.lang)
			if err == nil {
				t.Errorf("expected error for invalid version %q, got nil", tt.lang.Version)
			}
		})
	}
}

func TestMiseInstallBlock_RejectsInvalidVersion(t *testing.T) {
	tests := []struct {
		name  string
		langs []DetectedLang
	}{
		{"ruby injection", []DetectedLang{{Lang: LangRuby, Version: "3.3 && evil"}}},
		{"python injection", []DetectedLang{{Lang: LangPython, Version: "3.12 ; rm -rf /"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := miseInstallBlock(tt.langs)
			if err == nil {
				t.Errorf("expected error for invalid version, got nil")
			}
		})
	}
}
