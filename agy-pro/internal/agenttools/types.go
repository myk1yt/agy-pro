package agenttools

// ToolDef represents an OpenAI function tool definition.
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef defines the function signature in OpenAI format.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// BuildOpenAITools constructs the list of OpenAI function definitions based on
// permitted capabilities. By default, read-only tools (read_file, list_dir, grep, glob)
// are always available. write_file and run_command are included ONLY when explicitly permitted.
func BuildOpenAITools(allowWrite, allowExec bool) []ToolDef {
	tools := []ToolDef{
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "read_file",
				Description: "Read the content of a file within the workspace root, optionally specifying a 1-indexed line range.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Relative path to the file from workspace root.",
						},
						"start_line": map[string]any{
							"type":        "integer",
							"description": "Optional 1-indexed starting line number.",
						},
						"end_line": map[string]any{
							"type":        "integer",
							"description": "Optional 1-indexed ending line number.",
						},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "list_dir",
				Description: "List entries in a directory relative to the workspace root.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Relative directory path (defaults to '.' for root).",
						},
						"recursive": map[string]any{
							"type":        "boolean",
							"description": "Whether to list subdirectories recursively.",
						},
						"max_depth": map[string]any{
							"type":        "integer",
							"description": "Maximum depth for recursive listing (default 2).",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "grep",
				Description: "Search for regular expression or string pattern in workspace files.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "Regular expression pattern to search for.",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Directory or file path to search within (default '.').",
						},
						"case_insensitive": map[string]any{
							"type":        "boolean",
							"description": "Case insensitive search (default false).",
						},
						"max_matches": map[string]any{
							"type":        "integer",
							"description": "Maximum number of matching lines to return (default 100).",
						},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			Type: "function",
			Function: FunctionDef{
				Name:        "glob",
				Description: "Find files matching wildcard patterns (e.g. *.go, **/*.json) within workspace root.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{
							"type":        "string",
							"description": "Glob pattern (e.g., *.go, cmd/**/*.go).",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Starting directory (default '.').",
						},
					},
					"required": []string{"pattern"},
				},
			},
		},
	}

	if allowWrite {
		tools = append(tools, ToolDef{
			Type: "function",
			Function: FunctionDef{
				Name:        "write_file",
				Description: "Write or overwrite content to a file within the workspace root. Requires explicit write permission.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Relative path of the target file.",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Full file content to write.",
						},
						"overwrite": map[string]any{
							"type":        "boolean",
							"description": "Whether to overwrite existing file (default true).",
						},
					},
					"required": []string{"path", "content"},
				},
			},
		})
	}

	if allowExec {
		tools = append(tools, ToolDef{
			Type: "function",
			Function: FunctionDef{
				Name:        "run_command",
				Description: "Execute a command within workspace root. Requires explicit exec permission.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Command binary or script name to execute.",
						},
						"args": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "string",
							},
							"description": "Arguments to pass to the command.",
						},
						"cwd": map[string]any{
							"type":        "string",
							"description": "Working directory relative to root (default '.').",
						},
						"timeout_seconds": map[string]any{
							"type":        "integer",
							"description": "Timeout in seconds (default 30).",
						},
					},
					"required": []string{"command"},
				},
			},
		})
	}

	return tools
}
