// Package config reads the gx home, settings, and agent profiles.
// Sessions and credentials live under this home, not ~/.fx.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Settings is the on-disk configuration a person edits with fx config.
type Settings struct {
	Model          string   `json:"model,omitempty"`
	PermissionMode string   `json:"permission_mode,omitempty"`
	Provider       string   `json:"provider,omitempty"`
	BaseURL        string   `json:"base_url,omitempty"`
	Profile        string   `json:"profile,omitempty"`
	ServiceTier    string   `json:"service_tier,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	ExtraDirs      []string `json:"extra_dirs,omitempty"`
}

// Profile is an agent profile: tools, model, prompt, and permission mode.
type Profile struct {
	Name           string   `json:"name,omitempty"`
	Model          string   `json:"model,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	PermissionMode string   `json:"permission_mode,omitempty"`
	Tools          []string `json:"tools,omitempty"`
}

// MCPServer is one configured MCP server.
type MCPServer struct {
	Name     string   `json:"name"`
	Command  string   `json:"command,omitempty"`
	Args     []string `json:"args,omitempty"`
	URL      string   `json:"url,omitempty"`
	Env      []string `json:"env,omitempty"`
	Token    string   `json:"token,omitempty"`
	Approved bool     `json:"approved,omitempty"`
	Rejected bool     `json:"rejected,omitempty"`
}

// File is settings plus the lists that live beside them.
type File struct {
	Settings
	MCPServers []MCPServer `json:"mcp_servers,omitempty"`
}

// Home is GX_HOME, or ~/.gx.
func Home() (string, error) {
	if h := os.Getenv("GX_HOME"); h != "" {
		return h, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".gx"), nil
}

func settingsPath(home string) string { return filepath.Join(home, "settings.json") }

// Load reads settings. A missing file is empty settings, not an error.
func Load(home string) (File, error) {
	var f File
	b, err := os.ReadFile(settingsPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if len(bytesTrim(b)) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	return f, nil
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// Save writes settings. The directory is 0700 and the file is 0600.
func Save(home string, f File) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(settingsPath(home), b, 0o600)
}

// Project is the repo-safe subset of .fx.json.
type Project struct {
	Context            string   `json:"context,omitempty"`
	MaxAgentSteps      int      `json:"max_agent_steps,omitempty"`
	MaxToolResultBytes int      `json:"max_tool_result_bytes,omitempty"`
	ProviderOrder      []string `json:"provider_order,omitempty"`
	ProviderStrict     bool     `json:"provider_strict,omitempty"`
}

// LoadProject reads <workspace>/.fx.json. A missing file is empty, not an error.
// Keys outside the repo-safe set are ignored.
func LoadProject(workspace string) (Project, error) {
	var p Project
	if workspace == "" {
		return p, nil
	}
	b, err := os.ReadFile(filepath.Join(workspace, ".fx.json"))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if len(bytesTrim(b)) == 0 {
		return p, nil
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, nil
}

// LoadProfile reads agents/<name>/profile.json plus prompt.md, then the
// legacy agents/<name>.json file. Workspace wins over the home directory.
func LoadProfile(home, workspace, name string) (Profile, error) {
	candidates := []string{
		filepath.Join(workspace, ".gx", "agents", name),
		filepath.Join(home, "agents", name),
		filepath.Join(workspace, ".gx", "agents", name+".json"),
		filepath.Join(home, "agents", name+".json"),
	}
	var last error
	for _, path := range candidates {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			last = err
			continue
		}
		if err != nil {
			return Profile{}, err
		}
		var b []byte
		var prompt string
		if info.IsDir() {
			b, err = os.ReadFile(filepath.Join(path, "profile.json"))
			if errors.Is(err, os.ErrNotExist) {
				last = err
				continue
			}
			if err != nil {
				return Profile{}, err
			}
			prompt, err = readIf(filepath.Join(path, "prompt.md"))
			if err != nil {
				return Profile{}, err
			}
		} else {
			b, err = os.ReadFile(path)
			if err != nil {
				return Profile{}, err
			}
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			return Profile{}, err
		}
		if prompt != "" {
			p.Prompt = prompt
		}
		if p.Name == "" {
			p.Name = name
		}
		return p, nil
	}
	if last == nil {
		last = os.ErrNotExist
	}
	return Profile{}, last
}

// ProjectInstructions joins AGENTS.md files from the home directory and
// from the workspace root down to cwd. Later files are more specific.
func ProjectInstructions(home, workspace, cwd string) (string, error) {
	var parts []string
	if text, err := readIf(filepath.Join(home, "AGENTS.md")); err != nil {
		return "", err
	} else if text != "" {
		parts = append(parts, text)
	}
	if workspace == "" {
		return strings.Join(parts, "\n\n"), nil
	}
	rel, err := filepath.Rel(workspace, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = "."
	}
	dir := workspace
	var chain []string
	chain = append(chain, filepath.Join(workspace, "AGENTS.md"))
	if rel != "." {
		acc := workspace
		for _, elem := range strings.Split(rel, string(filepath.Separator)) {
			if elem == "." || elem == "" {
				continue
			}
			acc = filepath.Join(acc, elem)
			chain = append(chain, filepath.Join(acc, "AGENTS.md"))
		}
	}
	_ = dir
	for _, path := range chain {
		text, err := readIf(path)
		if err != nil {
			return "", err
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func readIf(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
