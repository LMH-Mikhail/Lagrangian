// Package config manages the external YAML configuration for Lagrangian.
// Zero personal data or environment-specific paths are hardcoded.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lagrangian-dev/lagrangian/internal/platform/paths"
	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

type Config struct {
	Version    int              `yaml:"version" json:"version"`
	Server     ServerConfig     `yaml:"server" json:"server"`
	Storage    StorageConfig    `yaml:"storage" json:"storage"`
	Workflow   WorkflowConfig   `yaml:"workflow" json:"workflow"`
	Git        GitConfig        `yaml:"git" json:"git"`
	Components ComponentsConfig `yaml:"components" json:"components"`
}

type ServerConfig struct {
	Mode        string `yaml:"mode" json:"mode"`                     // "standalone" | "server"
	Host        string `yaml:"host,omitempty" json:"host,omitempty"` // Optional override; empty derives from mode
	Port        int    `yaml:"port" json:"port"`
	AuthEnabled bool   `yaml:"auth_enabled" json:"auth_enabled"`
	APITokenRef string `yaml:"api_token_ref" json:"api_token_ref"`
}

// EffectiveHost returns the listen host derived from Mode unless explicitly overridden.
func (s ServerConfig) EffectiveHost() string {
	if h := strings.TrimSpace(s.Host); h != "" {
		return h
	}
	if s.Mode == "server" {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

type StorageConfig struct {
	DataDir        string `yaml:"data_dir" json:"data_dir"`
	DBPath         string `yaml:"db_path" json:"db_path"`
	AttachmentsDir string `yaml:"attachments_dir" json:"attachments_dir"`
}

type StatusSpec struct {
	Key      string `yaml:"key" json:"key"`
	Label    string `yaml:"label" json:"label"`
	Category string `yaml:"category" json:"category"` // "todo" | "active" | "blocked" | "done"
	Color    string `yaml:"color" json:"color"`
}

type WorkflowConfig struct {
	TaskStatuses []StatusSpec `yaml:"task_statuses" json:"task_statuses"`
}

type GitConfig struct {
	AuthorAliases       []string `yaml:"author_aliases" json:"author_aliases"`
	AutoReconcileRebase bool     `yaml:"auto_reconcile_rebase" json:"auto_reconcile_rebase"`
}

// ComponentSlot is a generic declarative slot for pluggable components
// activated across Phases 2-4.
type ComponentSlot struct {
	ID        string            `yaml:"id" json:"id"`
	Name      string            `yaml:"name" json:"name"`
	Driver    string            `yaml:"driver" json:"driver"`
	Enabled   bool              `yaml:"enabled" json:"enabled"`
	SecretRef string            `yaml:"secret_ref,omitempty" json:"secret_ref,omitempty"`
	Options   map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

type MCPConfig struct {
	Enabled    bool   `yaml:"enabled" json:"enabled"`
	Transport  string `yaml:"transport" json:"transport"` // "stdio" | "http_sse"
	AllowWrite bool   `yaml:"allow_write" json:"allow_write"`
}

type ComponentsConfig struct {
	CodeHosts      []ComponentSlot `yaml:"code_hosts" json:"code_hosts"`
	IssueTrackers  []ComponentSlot `yaml:"issue_trackers" json:"issue_trackers"`
	Bots           []ComponentSlot `yaml:"bots" json:"bots"`
	AgentSources   []ComponentSlot `yaml:"agent_sources" json:"agent_sources"`
	LLMProviders   []ComponentSlot `yaml:"llm_providers" json:"llm_providers"`
	KnowledgeBases []ComponentSlot `yaml:"knowledge_bases" json:"knowledge_bases"`
	MCP            MCPConfig       `yaml:"mcp" json:"mcp"`
}

// Default returns a clean, portable baseline configuration with zero personal data.
func Default(layout paths.Layout) Config {
	return Config{
		Version: CurrentVersion,
		Server: ServerConfig{
			Mode:        "standalone",
			Port:        19000,
			AuthEnabled: false,
		},
		Storage: StorageConfig{
			DataDir:        layout.DataDir,
			DBPath:         layout.DBPath,
			AttachmentsDir: layout.AttachmentsDir,
		},
		Workflow: WorkflowConfig{
			TaskStatuses: DefaultStatuses(),
		},
		Git: GitConfig{
			AuthorAliases:       []string{},
			AutoReconcileRebase: true,
		},
		Components: ComponentsConfig{
			CodeHosts:      []ComponentSlot{},
			IssueTrackers:  []ComponentSlot{},
			Bots:           []ComponentSlot{},
			AgentSources:   []ComponentSlot{},
			LLMProviders:   []ComponentSlot{},
			KnowledgeBases: []ComponentSlot{},
			MCP: MCPConfig{
				Enabled:    false,
				Transport:  "http_sse",
				AllowWrite: false,
			},
		},
	}
}

func DefaultStatuses() []StatusSpec {
	return []StatusSpec{
		{Key: "backlog", Label: "待规划", Category: "todo", Color: "#8b949e"},
		{Key: "ready", Label: "就绪", Category: "todo", Color: "#58a6ff"},
		{Key: "in_progress", Label: "进行中", Category: "active", Color: "#3fb950"},
		{Key: "blocked", Label: "阻塞中", Category: "blocked", Color: "#f85149"},
		{Key: "in_review", Label: "待验收", Category: "active", Color: "#d29922"},
		{Key: "done", Label: "已完成", Category: "done", Color: "#a371f7"},
		{Key: "canceled", Label: "已取消", Category: "done", Color: "#6e7681"},
	}
}

// Load reads config from filePath and normalizes empty defaults against layout.
// Returns (cfg, exists, err). When exists is false, cfg holds Default(layout).
func Load(filePath string, layout paths.Layout) (Config, bool, error) {
	cfg := Default(layout)
	raw, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, false, nil
		}
		return cfg, false, fmt.Errorf("read config %s: %w", filePath, err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return cfg, true, fmt.Errorf("parse yaml config %s: %w", filePath, err)
	}
	cfg.Normalize(layout)
	if err := cfg.Validate(); err != nil {
		return cfg, true, err
	}
	return cfg, true, nil
}

// Save writes cfg atomically to filePath.
func Save(filePath string, cfg Config, layout paths.Layout) error {
	cfg.Normalize(layout)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp := filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	return os.Rename(tmp, filePath)
}

func (c *Config) Normalize(layout paths.Layout) {
	if c.Version == 0 {
		c.Version = CurrentVersion
	}
	if c.Server.Mode == "" {
		c.Server.Mode = "standalone"
	}
	c.Server.Host = strings.TrimSpace(c.Server.Host)
	if c.Server.Port == 0 {
		c.Server.Port = 19000
	}
	if c.Storage.DataDir == "" {
		c.Storage.DataDir = layout.DataDir
	}
	if c.Storage.DBPath == "" {
		c.Storage.DBPath = filepath.Join(c.Storage.DataDir, "lagrangian.sqlite")
	}
	if c.Storage.AttachmentsDir == "" {
		c.Storage.AttachmentsDir = filepath.Join(c.Storage.DataDir, "attachments")
	}
	if len(c.Workflow.TaskStatuses) == 0 {
		c.Workflow.TaskStatuses = DefaultStatuses()
	}
	if c.Git.AuthorAliases == nil {
		c.Git.AuthorAliases = []string{}
	}
	if c.Components.CodeHosts == nil {
		c.Components.CodeHosts = []ComponentSlot{}
	}
	if c.Components.IssueTrackers == nil {
		c.Components.IssueTrackers = []ComponentSlot{}
	}
	if c.Components.Bots == nil {
		c.Components.Bots = []ComponentSlot{}
	}
	if c.Components.AgentSources == nil {
		c.Components.AgentSources = []ComponentSlot{}
	}
	if c.Components.LLMProviders == nil {
		c.Components.LLMProviders = []ComponentSlot{}
	}
	if c.Components.KnowledgeBases == nil {
		c.Components.KnowledgeBases = []ComponentSlot{}
	}
	if c.Components.MCP.Transport == "" {
		c.Components.MCP.Transport = "http_sse"
	}
}

func (c Config) Validate() error {
	if c.Server.Mode != "standalone" && c.Server.Mode != "server" {
		return fmt.Errorf("invalid server.mode %q: must be standalone or server", c.Server.Mode)
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("invalid server.port %d: must be in 1..65535", c.Server.Port)
	}
	seen := map[string]bool{}
	for _, st := range c.Workflow.TaskStatuses {
		key := strings.TrimSpace(st.Key)
		if key == "" {
			return errors.New("workflow.task_statuses contains empty key")
		}
		if seen[key] {
			return fmt.Errorf("duplicate workflow status key %q", key)
		}
		seen[key] = true
	}
	return nil
}

// HasStatus reports whether key exists in the configured workflow statuses.
func (c Config) HasStatus(key string) bool {
	for _, st := range c.Workflow.TaskStatuses {
		if st.Key == key {
			return true
		}
	}
	return false
}

// StatusCategory returns the category ("todo"|"active"|"blocked"|"done") for key.
func (c Config) StatusCategory(key string) string {
	for _, st := range c.Workflow.TaskStatuses {
		if st.Key == key {
			return st.Category
		}
	}
	return ""
}
