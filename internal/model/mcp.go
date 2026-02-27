package model

// MCPServer represents an MCP server configuration
type MCPServer struct {
	Name    string   `json:"name"`    // Unique identifier for the server
	Command string   `json:"command"` // Executable command (e.g., "npx", "node")
	Args    []string `json:"args"`    // Command arguments
}
