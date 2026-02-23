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
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangGo, Version: "1.23"},
	}, "0.2.11")
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
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangGo, Version: "1.22"},
		{Lang: LangRuby, Version: "3.3"},
		{Lang: LangNode, Version: "20"},
	}, "0.2.11")
	expected := fmt.Sprintf("go1.22.0.linux-%s.tar.gz", goArch())
	if !strings.Contains(df, expected) {
		t.Errorf("expected %s in Dockerfile", expected)
	}
	if !strings.Contains(df, "ruby-install --system ruby 3.3") {
		t.Error("expected Ruby 3.3 install in Dockerfile")
	}
	if !strings.Contains(df, "setup_20.x") {
		t.Error("expected Node 20 in base layer")
	}
}

func TestGenerateDockerfile_NoLanguages(t *testing.T) {
	df := GenerateDockerfile(nil, "0.2.11")
	if !strings.Contains(df, "ubuntu:24.04") {
		t.Error("expected ubuntu base image")
	}
	if !strings.Contains(df, "claude-code") {
		t.Error("expected Claude Code install")
	}
	// Should use default Node version
	if !strings.Contains(df, "setup_20.x") {
		t.Error("expected default Node 20 in base layer")
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
			df := GenerateDockerfile(tt.langs, "0.2.11")
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
			df := GenerateDockerfile(tt.langs, "0.2.11")
			if !strings.Contains(df, "entrypoint.sh") {
				t.Error("expected entrypoint script in every Dockerfile")
			}
			if !strings.Contains(df, "npm update -g @anthropic-ai/claude-code") {
				t.Error("expected Claude Code update in entrypoint script")
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
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangGo, Version: ""},
	}, "0.2.11")
	// Should use default Go version 1.23
	expected := fmt.Sprintf("go1.23.0.linux-%s.tar.gz", goArch())
	if !strings.Contains(df, expected) {
		t.Errorf("expected %s when version is empty", expected)
	}
}

func TestGenerateDockerfile_PythonWithVersion(t *testing.T) {
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangPython, Version: "3.11"},
	}, "0.2.11")
	if !strings.Contains(df, "python3.11") {
		t.Error("expected Python 3.11 install in Dockerfile")
	}
	if !strings.Contains(df, "deadsnakes") {
		t.Error("expected deadsnakes PPA in Dockerfile")
	}
}

func TestGenerateDockerfile_RustWithVersion(t *testing.T) {
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangRust, Version: "1.77.0"},
	}, "0.2.11")
	if !strings.Contains(df, "--default-toolchain 1.77.0") {
		t.Error("expected Rust 1.77.0 toolchain in Dockerfile")
	}
	if !strings.Contains(df, "rustup.rs") {
		t.Error("expected rustup install in Dockerfile")
	}
}

func TestGenerateDockerfile_JavaWithVersion(t *testing.T) {
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangJava, Version: "21"},
	}, "0.2.11")
	if !strings.Contains(df, "openjdk-21-jdk") {
		t.Error("expected OpenJDK 21 install in Dockerfile")
	}
}

func TestGenerateDockerfile_PHP(t *testing.T) {
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangPHP},
	}, "0.2.11")
	if !strings.Contains(df, "php-cli") {
		t.Error("expected PHP CLI install in Dockerfile")
	}
	if !strings.Contains(df, "composer") {
		t.Error("expected Composer install in Dockerfile")
	}
}

func TestGenerateDockerfile_IncludesPluralBinary(t *testing.T) {
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
			df := GenerateDockerfile(tt.langs, "0.2.11")
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
			df := GenerateDockerfile(nil, version)
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
	df1 := GenerateDockerfile(langs, "0.2.11")
	df2 := GenerateDockerfile(langs, "0.2.11")
	if df1 != df2 {
		t.Error("GenerateDockerfile should be deterministic for the same input")
	}
}

func TestGenerateDockerfile_NodeVersionOverride(t *testing.T) {
	// When Node is detected with a specific version, it should use that version
	df := GenerateDockerfile([]DetectedLang{
		{Lang: LangNode, Version: "22"},
	}, "0.2.11")
	if !strings.Contains(df, "setup_22.x") {
		t.Error("expected detected Node version 22 to override default")
	}
	if strings.Contains(df, "setup_20.x") {
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
	tag, err := EnsureImage(context.Background(), []DetectedLang{{Lang: LangGo, Version: "1.23"}}, "0.2.11", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inspectCalled {
		t.Error("expected docker image inspect to be called")
	}
	if !strings.HasPrefix(tag, "erg:") {
		t.Errorf("expected tag to start with 'erg:', got %q", tag)
	}
}

func TestEnsureImage_BuildsWhenNotCached(t *testing.T) {
	orig := dockerCommandFunc
	defer func() { dockerCommandFunc = orig }()

	buildCalled := false
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
			return []byte("built"), nil
		}
		return nil, fmt.Errorf("unexpected call: %v", args)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	tag, err := EnsureImage(context.Background(), []DetectedLang{{Lang: LangGo, Version: "1.23"}}, "0.2.11", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !buildCalled {
		t.Error("expected docker build to be called")
	}
	if !strings.HasPrefix(tag, "erg:") {
		t.Errorf("expected valid tag, got %q", tag)
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
	_, err := EnsureImage(context.Background(), nil, "0.2.11", logger)
	if err == nil {
		t.Fatal("expected error on build failure")
	}
	if !strings.Contains(err.Error(), "docker build failed") {
		t.Errorf("expected 'docker build failed' in error, got: %v", err)
	}
}

func TestEnsureImage_DevUsesCaching(t *testing.T) {
	orig := dockerCommandFunc
	defer func() { dockerCommandFunc = orig }()

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
	_, err := EnsureImage(context.Background(), nil, "dev", logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !inspectCalled {
		t.Error("dev builds should still check image cache")
	}
}
