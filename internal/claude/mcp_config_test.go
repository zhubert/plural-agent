package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateContainerMCPConfigLocked(t *testing.T) {
	r := &Runner{
		sessionID: "test-container-mcp",
		log:       testLogger(),
	}

	// Use a container port (what ensureServerRunning passes for container sessions)
	containerPort := 21120
	configPath, err := r.createContainerMCPConfigLocked(containerPort)
	if err != nil {
		t.Fatalf("createContainerMCPConfigLocked() error = %v", err)
	}
	defer os.Remove(configPath)

	// Read and parse the config
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config JSON: %v", err)
	}

	// Verify mcpServers structure
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key in config")
	}

	ergServer, ok := mcpServers["erg"].(map[string]any)
	if !ok {
		t.Fatal("expected 'erg' server in mcpServers")
	}

	// Verify command points to in-container binary
	command, ok := ergServer["command"].(string)
	if !ok {
		t.Fatal("expected 'command' field")
	}
	if command != "/usr/local/bin/erg" {
		t.Errorf("command = %q, want '/usr/local/bin/erg'", command)
	}

	// Verify args include --auto-approve and --listen (not --socket or --tcp)
	argsRaw, ok := ergServer["args"].([]any)
	if !ok {
		t.Fatal("expected 'args' field to be array")
	}

	args := make([]string, len(argsRaw))
	for i, a := range argsRaw {
		args[i], _ = a.(string)
	}

	if !containsArg(args, "--auto-approve") {
		t.Error("container MCP config should include --auto-approve flag")
	}
	if !containsArg(args, "--listen") {
		t.Error("container MCP config should include --listen flag")
	}
	if containsArg(args, "--tcp") {
		t.Error("container MCP config should NOT include --tcp flag (--listen is used instead)")
	}
	if containsArg(args, "--socket") {
		t.Error("container MCP config should NOT include --socket flag")
	}
	if got := getArgValue(args, "--listen"); got != "0.0.0.0:21120" {
		t.Errorf("--listen value = %q, want %q", got, "0.0.0.0:21120")
	}
	if !containsArg(args, "mcp-server") {
		t.Error("container MCP config should include 'mcp-server' subcommand")
	}
	if !containsArg(args, "--session-id") {
		t.Error("container MCP config should include --session-id flag")
	}
	if got := getArgValue(args, "--session-id"); got != "test-container-mcp" {
		t.Errorf("--session-id value = %q, want %q", got, "test-container-mcp")
	}
}

func TestCreateContainerMCPConfig_WithAllowedTools(t *testing.T) {
	r := &Runner{
		sessionID:    "test-planning-mcp",
		allowedTools: []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"},
		log:          testLogger(),
	}

	configPath, err := r.createContainerMCPConfigLocked(21120)
	if err != nil {
		t.Fatalf("createContainerMCPConfigLocked() error = %v", err)
	}
	defer os.Remove(configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config JSON: %v", err)
	}

	mcpServers := config["mcpServers"].(map[string]any)
	ergServer := mcpServers["erg"].(map[string]any)
	argsRaw := ergServer["args"].([]any)
	args := make([]string, len(argsRaw))
	for i, a := range argsRaw {
		args[i], _ = a.(string)
	}

	if !containsArg(args, "--allowed-tools") {
		t.Fatal("expected --allowed-tools flag in container MCP config")
	}

	got := getArgValue(args, "--allowed-tools")
	want := "Read,Glob,Grep,WebFetch,WebSearch"
	if got != want {
		t.Errorf("--allowed-tools = %q, want %q", got, want)
	}
}

func TestCreateContainerMCPConfig_NoAllowedToolsOmitsFlag(t *testing.T) {
	r := &Runner{
		sessionID: "test-coding-mcp",
		log:       testLogger(),
	}

	configPath, err := r.createContainerMCPConfigLocked(21120)
	if err != nil {
		t.Fatalf("createContainerMCPConfigLocked() error = %v", err)
	}
	defer os.Remove(configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config JSON: %v", err)
	}

	mcpServers := config["mcpServers"].(map[string]any)
	ergServer := mcpServers["erg"].(map[string]any)
	argsRaw := ergServer["args"].([]any)
	args := make([]string, len(argsRaw))
	for i, a := range argsRaw {
		args[i], _ = a.(string)
	}

	if containsArg(args, "--allowed-tools") {
		t.Error("coding sessions should NOT include --allowed-tools (uses wildcard auto-approve)")
	}
}

func TestCreateMCPConfigLocked_HostSession(t *testing.T) {
	r := &Runner{
		sessionID: "test-host-mcp",
		log:       testLogger(),
	}

	socketPath := "/tmp/pl-test-host-mcp.sock"
	configPath, err := r.createMCPConfigLocked(socketPath)
	if err != nil {
		t.Fatalf("createMCPConfigLocked() error = %v", err)
	}
	defer os.Remove(configPath)

	// Read and parse the config
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config JSON: %v", err)
	}

	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("expected mcpServers key in config")
	}

	ergServer, ok := mcpServers["erg"].(map[string]any)
	if !ok {
		t.Fatal("expected 'erg' server in mcpServers")
	}

	// Host config should NOT use /usr/local/bin/erg
	command, ok := ergServer["command"].(string)
	if !ok {
		t.Fatal("expected 'command' field")
	}
	if command == "/usr/local/bin/erg" {
		t.Error("host MCP config should use the current executable, not /usr/local/bin/erg")
	}

	// Host config should NOT include --auto-approve
	argsRaw, ok := ergServer["args"].([]any)
	if !ok {
		t.Fatal("expected 'args' field to be array")
	}
	args := make([]string, len(argsRaw))
	for i, a := range argsRaw {
		args[i], _ = a.(string)
	}
	if containsArg(args, "--auto-approve") {
		t.Error("host MCP config should NOT include --auto-approve flag")
	}
}

func TestCreateContainerMCPConfig_WithExternalServers(t *testing.T) {
	// Container MCP config should include external MCP servers when configured.
	// Commands must be available inside the container image.
	r := &Runner{
		sessionID: "test-external-servers",
		log:       testLogger(),
		mcpServers: []MCPServer{
			{Name: "my-db-tool", Command: "npx", Args: []string{"-y", "@myorg/db-mcp-server"}},
			{Name: "k8s-context", Command: "/usr/local/bin/kubectl-mcp", Args: []string{"--readonly"}},
		},
	}

	configPath, err := r.createContainerMCPConfigLocked(21120)
	if err != nil {
		t.Fatalf("createContainerMCPConfigLocked() error = %v", err)
	}
	defer os.Remove(configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config JSON: %v", err)
	}

	mcpServers := config["mcpServers"].(map[string]any)

	// erg entry must still be present
	if _, ok := mcpServers["erg"]; !ok {
		t.Fatal("expected 'erg' server in mcpServers")
	}

	// External servers must be included
	dbServer, ok := mcpServers["my-db-tool"].(map[string]any)
	if !ok {
		t.Fatal("expected 'my-db-tool' server in mcpServers")
	}
	if dbServer["command"] != "npx" {
		t.Errorf("my-db-tool command = %q, want 'npx'", dbServer["command"])
	}
	argsRaw, _ := dbServer["args"].([]any)
	if len(argsRaw) != 2 || argsRaw[0] != "-y" || argsRaw[1] != "@myorg/db-mcp-server" {
		t.Errorf("my-db-tool args = %v, want [-y @myorg/db-mcp-server]", argsRaw)
	}

	k8sServer, ok := mcpServers["k8s-context"].(map[string]any)
	if !ok {
		t.Fatal("expected 'k8s-context' server in mcpServers")
	}
	if k8sServer["command"] != "/usr/local/bin/kubectl-mcp" {
		t.Errorf("k8s-context command = %q, want '/usr/local/bin/kubectl-mcp'", k8sServer["command"])
	}
}

func TestFindMCPConfigFiles_Empty(t *testing.T) {
	setupAuthTest(t) // reuse XDG isolation from container_auth_test.go

	files, err := FindMCPConfigFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 MCP config files, got %d", len(files))
	}
}

func TestFindMCPConfigFiles_ReturnsMatches(t *testing.T) {
	configDir := setupAuthTest(t)

	for _, name := range []string{"erg-mcp-aaa.json", "erg-mcp-bbb.json"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("failed to create MCP config file: %v", err)
		}
	}
	// Non-matching file
	os.WriteFile(filepath.Join(configDir, "config.json"), []byte("{}"), 0o644)

	files, err := FindMCPConfigFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 MCP config files, got %d", len(files))
	}
}

func TestClearMCPConfigFiles_RemovesAll(t *testing.T) {
	configDir := setupAuthTest(t)

	for _, name := range []string{"erg-mcp-aaa.json", "erg-mcp-bbb.json", "erg-mcp-ccc.json"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("{}"), 0o600); err != nil {
			t.Fatalf("failed to create MCP config file: %v", err)
		}
	}
	// Non-matching file that should survive
	otherFile := filepath.Join(configDir, "config.json")
	os.WriteFile(otherFile, []byte("{}"), 0o644)

	count, err := ClearMCPConfigFiles()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 files removed, got %d", count)
	}

	remaining, _ := FindMCPConfigFiles()
	if len(remaining) != 0 {
		t.Errorf("expected 0 remaining MCP config files, got %d", len(remaining))
	}

	if _, err := os.Stat(otherFile); err != nil {
		t.Error("expected config.json to still exist")
	}
}
