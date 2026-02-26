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
// from the GitHub release and installed as /usr/local/bin/erg.
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

	// Base layer: node Alpine image + essential tools + Claude Code.
	// Alpine is significantly smaller than Ubuntu (~5MB vs ~80MB base).
	// Node.js is always required for Claude Code, so node:XX-alpine is the natural base.
	fmt.Fprintf(&b, "FROM node:%s-alpine\n", nodeVersion)
	b.WriteString("RUN apk add --no-cache git curl ca-certificates build-base gnupg bash\n")
	b.WriteString("RUN npm install -g @anthropic-ai/claude-code\n")

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
	// Entrypoint script: install latest Claude Code on boot (keeps cached image fresh),
	// then exec into claude with all original arguments.
	// Uses `npm install @latest` instead of `npm update` because npm update respects
	// semver ranges and won't cross major version boundaries.
	// Redirects both stdout and stderr to avoid polluting the JSON stream.
	// Checks ERG_SKIP_UPDATE to allow developers to skip the update.
	b.WriteString("RUN printf '#!/bin/sh\\nif [ -z \"$ERG_SKIP_UPDATE\" ]; then npm install -g @anthropic-ai/claude-code@latest >/dev/null 2>&1; fi\\nexec claude \"$@\"\\n' > /usr/local/bin/entrypoint.sh \\\n")
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
			"RUN apk add --no-cache autoconf bison openssl-dev yaml-dev readline-dev zlib-dev libffi-dev linux-headers \\\n"+
			"    && curl -fsSL https://github.com/postmodern/ruby-install/releases/download/v0.9.3/ruby-install-0.9.3.tar.gz | tar -xz \\\n"+
			"    && cd ruby-install-0.9.3 && make install && cd .. && rm -rf ruby-install-0.9.3 \\\n"+
			"    && ruby-install --system ruby %s\n",
			v)
	case LangPython:
		// Use pyenv to install the specific Python version requested.
		// Alpine only ships one system python3 with no version pinning support.
		return fmt.Sprintf(""+
			"RUN apk add --no-cache libffi-dev openssl-dev bzip2-dev xz-dev readline-dev sqlite-dev \\\n"+
			"    && curl -fsSL https://pyenv.run | bash \\\n"+
			"    && export PYENV_ROOT=\"/root/.pyenv\" \\\n"+
			"    && export PATH=\"$PYENV_ROOT/bin:$PATH\" \\\n"+
			"    && pyenv install %s \\\n"+
			"    && pyenv global %s\n"+
			"ENV PYENV_ROOT=\"/root/.pyenv\"\n"+
			"ENV PATH=\"/root/.pyenv/shims:/root/.pyenv/bin:${PATH}\"\n",
			v, v)
	case LangRust:
		return fmt.Sprintf(""+
			"RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain %s\n"+
			"ENV PATH=\"/root/.cargo/bin:${PATH}\"\n",
			v)
	case LangJava:
		return fmt.Sprintf(""+
			"RUN apk add --no-cache openjdk%s-jdk\n",
			v)
	case LangPHP:
		// Alpine uses versioned PHP package names (e.g., php83, php83-cli).
		// Default to PHP 8.3; php83-phar and php83-openssl are required by Composer.
		return "" +
			"RUN apk add --no-cache php83 php83-cli php83-mbstring php83-xml php83-phar php83-openssl \\\n" +
			"    && ln -s /usr/bin/php83 /usr/bin/php \\\n" +
			"    && curl -fsSL https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer\n"
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

// ImageExists reports whether the container image for the given languages
// and version is already built and cached locally.
func ImageExists(ctx context.Context, langs []DetectedLang, version string) bool {
	dockerfile := GenerateDockerfile(langs, version)
	tag := ImageTag(dockerfile)
	_, err := dockerCommandFunc(ctx, "", "image", "inspect", tag)
	return err == nil
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
