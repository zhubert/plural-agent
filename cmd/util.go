package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// lookPathFunc is the function used to look up binaries on PATH.
// Overridden in tests to control behavior.
var lookPathFunc = exec.LookPath

// hasContainerRuntime checks whether a container runtime binary (docker or colima)
// is available on PATH. Returns true if either is found.
func hasContainerRuntime() bool {
	if _, err := lookPathFunc("docker"); err == nil {
		return true
	}
	if _, err := lookPathFunc("colima"); err == nil {
		return true
	}
	return false
}

// checkDockerDaemon verifies a container runtime daemon is reachable, not just
// that the binary exists. This catches the case where Docker/Colima is installed
// but not running, which would otherwise cause silent per-session failures.
// Works with both Docker Desktop and Colima since both expose a Docker-compatible API.
func checkDockerDaemon() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		hint := runtimeStartHint()
		return fmt.Errorf("container runtime is not reachable (is Colima or Docker Desktop running?)%s", hint)
	}
	return nil
}

// runtimeStartHint returns a help message suggesting how to start a container runtime.
// It checks whether colima is installed to tailor the suggestion.
func runtimeStartHint() string {
	if _, err := lookPathFunc("colima"); err == nil {
		return "\n\nStart with: colima start"
	}
	return "\n\nInstall a container runtime:\n  Docker Desktop: https://docs.docker.com/get-docker/\n  Colima:          https://github.com/abiosoft/colima"
}

// confirm prompts the user for y/n confirmation
func confirm(input io.Reader, prompt string) bool {
	reader := bufio.NewReader(input)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.ToLower(strings.TrimSpace(response))
	return response == "y" || response == "yes"
}
