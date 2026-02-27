package model

import (
	"encoding/json"
	"testing"
)

func TestMCPServer_JSONRoundTrip(t *testing.T) {
	server := MCPServer{
		Name:    "test-server",
		Command: "npx",
		Args:    []string{"-y", "some-package"},
	}

	data, err := json.Marshal(server)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got MCPServer
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Name != server.Name {
		t.Errorf("name: got %q, want %q", got.Name, server.Name)
	}
	if got.Command != server.Command {
		t.Errorf("command: got %q, want %q", got.Command, server.Command)
	}
	if len(got.Args) != len(server.Args) {
		t.Fatalf("args length: got %d, want %d", len(got.Args), len(server.Args))
	}
	for i, arg := range got.Args {
		if arg != server.Args[i] {
			t.Errorf("args[%d]: got %q, want %q", i, arg, server.Args[i])
		}
	}
}
