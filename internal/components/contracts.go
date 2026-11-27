// Package components defines the pluggable extension contracts and runtime
// registry for Lagrangian. Concrete adapters (GitLab, Jira, WeCom, Codex,
// Antigravity, LLM providers, local/remote KnowledgeBases) register against
// these interfaces without coupling to core business logic.
package components

import (
	"context"
	"time"
)

type Kind string

const (
	KindCodeHost      Kind = "code_host"
	KindIssueTracker  Kind = "issue_tracker"
	KindBotChannel    Kind = "bot_channel"
	KindAgentSource   Kind = "agent_source"
	KindLLMProvider   Kind = "llm_provider"
	KindKnowledgeBase Kind = "knowledge_base"
	KindMCPGateway    Kind = "mcp_gateway"
)

type HealthState string

const (
	HealthReady    HealthState = "ready"
	HealthDegraded HealthState = "degraded"
	HealthDisabled HealthState = "disabled"
	HealthPlanned  HealthState = "planned"
)

type Status struct {
	ID        string      `json:"id"`
	Kind      Kind        `json:"kind"`
	Driver    string      `json:"driver"`
	Name      string      `json:"name"`
	Enabled   bool        `json:"enabled"`
	State     HealthState `json:"state"`
	Detail    string      `json:"detail"`
	CheckedAt time.Time   `json:"checked_at"`
}

// Component is the base lifecycle contract implemented by every plugin.
type Component interface {
	ID() string
	Kind() Kind
	Driver() string
	Name() string
	Health(ctx context.Context) Status
}

// CodeHost abstracts remote Git platforms (GitLab, GitHub, Gitea, etc.).
type CodeHost interface {
	Component
	ListRemoteCommits(ctx context.Context, project, branch string, authors []string) ([]RemoteCommit, error)
	ListMergeRequests(ctx context.Context, project string, state string) ([]MergeRequest, error)
}

type RemoteCommit struct {
	SHA         string `json:"sha"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	CommittedAt string `json:"committed_at"`
	WebURL      string `json:"web_url"`
}

type MergeRequest struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	State        string `json:"state"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	WebURL       string `json:"web_url"`
}

// IssueTracker abstracts external issue systems (Jira, Linear, GitHub Issues, etc.).
type IssueTracker interface {
	Component
	SearchIssues(ctx context.Context, query IssueQuery) ([]IssueItem, error)
}

type IssueQuery struct {
	ProjectKey string   `json:"project_key"`
	Statuses   []string `json:"statuses"`
	Assignee   string   `json:"assignee"`
	Keyword    string   `json:"keyword"`
}

type IssueItem struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
	WebURL   string `json:"web_url"`
}

// BotChannel abstracts multi-instance IM bot connectors (WeCom, Feishu, Slack, etc.).
type BotChannel interface {
	Component
	Roles() []string
	SendMarkdown(ctx context.Context, target, markdown string) error
}

// AgentSource abstracts local or cross-machine coding agent thread readers (Codex, Antigravity, etc.).
type AgentSource interface {
	Component
	ListThreads(ctx context.Context) ([]AgentThreadMeta, error)
}

type AgentThreadMeta struct {
	ID            string    `json:"id"`
	WorkstationID string    `json:"workstation_id,omitempty"`
	Source        string    `json:"source"`
	Title         string    `json:"title"`
	Workspace     string    `json:"workspace"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// LLMProvider abstracts local (Ollama, CLI) and remote (OpenAI-compatible) LLM backends.
type LLMProvider interface {
	Component
	Generate(ctx context.Context, prompt string, opts GenerateOptions) (string, error)
}

type GenerateOptions struct {
	Model       string  `json:"model"`
	System      string  `json:"system"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

// KnowledgeSource abstracts local directories and remote Git knowledge repositories.
type KnowledgeSource interface {
	Component
	Sync(ctx context.Context) error
	Search(ctx context.Context, query string) ([]KnowledgeHit, error)
}

type KnowledgeHit struct {
	BaseID  string `json:"base_id"`
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}
