package container

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
)

// defaultVersions are used when a version cannot be parsed from the repo.
var defaultVersions = map[Language]string{
	LangGo:     "1.23",
	LangNode:   "20",
	LangPython: "3.12",
	LangRuby:   "3.3",
	LangRust:   "1.77",
	LangJava:   "21",
}

// goArch returns the Go/Docker architecture string for the current platform.
func goArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

// releaseArch returns the goreleaser archive architecture suffix.
func releaseArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "x86_64"
	}
}

// GenerateDockerfile produces a Dockerfile that includes all detected language
// toolchains at their specified versions (or defaults).
// Node.js is always included since it's required for Claude Code.
// If version is non-empty (and not "dev"), the erg binary is downloaded
// from the GitHub release and installed as /usr/local/bin/plural.
func GenerateDockerfile(langs []DetectedLang, version string) string {
	var b strings.Builder

	// Determine Node version: use detected version if present, else default
	nodeVersion := defaultVersions[LangNode]
	for _, l := range langs {
		if l.Lang == LangNode && l.Version != "" {
			nodeVersion = l.Version
			break
		}
	}

	// Base layer: Ubuntu + essential tools + Node.js + Claude Code
	b.WriteString("FROM ubuntu:24.04\n")
	b.WriteString("ENV DEBIAN_FRONTEND=noninteractive\n")
	b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
	b.WriteString("    git curl ca-certificates build-essential gnupg \\\n")
	fmt.Fprintf(&b, "    && curl -fsSL https://deb.nodesource.com/setup_%s.x | bash - \\\n", nodeVersion)
	b.WriteString("    && apt-get install -y nodejs \\\n")
	b.WriteString("    && npm install -g @anthropic-ai/claude-code \\\n")
	b.WriteString("    && apt-get clean && rm -rf /var/lib/apt/lists/*\n")

	// Add language-specific install blocks
	for _, l := range langs {
		block := languageInstallBlock(l)
		if block != "" {
			b.WriteString(block)
		}
	}

	// Install the erg binary from GitHub releases.
	// Pinned versions use an exact tag; otherwise grab the latest release.
	if version != "" && version != "dev" {
		fmt.Fprintf(&b, "RUN curl -fsSL https://github.com/zhubert/erg/releases/download/v%s/erg_Linux_%s.tar.gz"+
			" | tar -xz -C /tmp && mv /tmp/erg /usr/local/bin/erg\n",
			version, releaseArch())
	} else {
		fmt.Fprintf(&b, "RUN curl -fsSL -L https://github.com/zhubert/erg/releases/latest/download/erg_Linux_%s.tar.gz"+
			" | tar -xz -C /tmp && mv /tmp/erg /usr/local/bin/erg\n",
			releaseArch())
	}

	// Entrypoint script: update Claude Code on boot (keeps cached image fresh),
	// then exec into claude with all original arguments.
	b.WriteString("RUN printf '#!/bin/sh\\nnpm update -g @anthropic-ai/claude-code 2>/dev/null\\nexec claude \"$@\"\\n' > /usr/local/bin/entrypoint.sh \\\n")
	b.WriteString("    && chmod +x /usr/local/bin/entrypoint.sh\n")
	b.WriteString("ENTRYPOINT [\"/usr/local/bin/entrypoint.sh\"]\n")

	return b.String()
}

// languageInstallBlock returns the Dockerfile RUN instruction for a language.
// Returns empty string if the language is handled in the base layer (Node) or unknown.
func languageInstallBlock(l DetectedLang) string {
	v := l.Version
	if v == "" {
		v = defaultVersions[l.Lang]
	}

	switch l.Lang {
	case LangGo:
		return fmt.Sprintf(""+
			"RUN curl -fsSL https://go.dev/dl/go%s.0.linux-%s.tar.gz | tar -C /usr/local -xz\n"+
			"ENV PATH=\"/usr/local/go/bin:/root/go/bin:${PATH}\"\n",
			v, goArch())
	case LangRuby:
		return fmt.Sprintf(""+
			"RUN apt-get update && apt-get install -y --no-install-recommends \\\n"+
			"    autoconf bison libssl-dev libyaml-dev libreadline-dev zlib1g-dev libffi-dev \\\n"+
			"    && curl -fsSL https://github.com/postmodern/ruby-install/releases/download/v0.9.3/ruby-install-0.9.3.tar.gz | tar -xz \\\n"+
			"    && cd ruby-install-0.9.3 && make install && cd .. && rm -rf ruby-install-0.9.3 \\\n"+
			"    && ruby-install --system ruby %s \\\n"+
			"    && apt-get clean && rm -rf /var/lib/apt/lists/*\n",
			v)
	case LangPython:
		return fmt.Sprintf(""+
			"RUN apt-get update && apt-get install -y --no-install-recommends software-properties-common \\\n"+
			"    && add-apt-repository -y ppa:deadsnakes/ppa \\\n"+
			"    && apt-get update && apt-get install -y --no-install-recommends \\\n"+
			"       python%s python%s-venv python%s-dev \\\n"+
			"    && apt-get clean && rm -rf /var/lib/apt/lists/*\n"+
			"RUN curl -fsSL https://bootstrap.pypa.io/get-pip.py | python%s\n",
			v, v, v, v)
	case LangRust:
		return fmt.Sprintf(""+
			"RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain %s\n"+
			"ENV PATH=\"/root/.cargo/bin:${PATH}\"\n",
			v)
	case LangJava:
		return fmt.Sprintf(""+
			"RUN apt-get update && apt-get install -y --no-install-recommends openjdk-%s-jdk \\\n"+
			"    && apt-get clean && rm -rf /var/lib/apt/lists/*\n",
			v)
	case LangPHP:
		return "" +
			"RUN apt-get update && apt-get install -y --no-install-recommends php php-cli php-mbstring php-xml \\\n" +
			"    && curl -fsSL https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer \\\n" +
			"    && apt-get clean && rm -rf /var/lib/apt/lists/*\n"
	case LangNode:
		// Handled in base layer
		return ""
	default:
		return ""
	}
}

// ImageTag returns a deterministic image tag based on the Dockerfile content.
// Format: erg:<first 12 chars of SHA256>
func ImageTag(dockerfile string) string {
	h := sha256.Sum256([]byte(dockerfile))
	return fmt.Sprintf("erg:%x", h[:6])
}

// dockerCommandFunc is the function used to execute docker commands. Overridden in tests.
var dockerCommandFunc = dockerCommand

func dockerCommand(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// EnsureImage generates a Dockerfile for the detected languages, builds it if
// not already cached, and returns the image tag. The erg binary is
// always installed from GitHub releases (pinned or latest).
func EnsureImage(ctx context.Context, langs []DetectedLang, version string, logger *slog.Logger) (string, error) {
	dockerfile := GenerateDockerfile(langs, version)
	tag := ImageTag(dockerfile)

	// Check if image already exists (cached)
	if _, err := dockerCommandFunc(ctx, "", "image", "inspect", tag); err == nil {
		logger.Info("using cached container image", "image", tag)
		return tag, nil
	}

	// Build the image
	langNames := make([]string, len(langs))
	for i, l := range langs {
		if l.Version != "" {
			langNames[i] = fmt.Sprintf("%s@%s", l.Lang, l.Version)
		} else {
			langNames[i] = string(l.Lang)
		}
	}
	logger.Info("building container image", "image", tag, "languages", strings.Join(langNames, ", "))

	_, err := dockerCommandFunc(ctx, dockerfile, "build", "-t", tag, "-f-", ".")
	if err != nil {
		return "", fmt.Errorf("docker build failed: %w", err)
	}

	logger.Info("container image built successfully", "image", tag)
	return tag, nil
}
