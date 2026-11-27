package domain

import "time"

// IntegrationConnection stores metadata for an external CodeHost or IssueTracker.
// Raw secrets are never stored here; only SecretRef is persisted.
type IntegrationConnection struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`      // "code_host" | "issue_tracker"
	Driver        string            `json:"driver"`    // "gitlab" | "jira"
	Name          string            `json:"name"`
	BaseURL       string            `json:"base_url"`
	AuthType      string            `json:"auth_type"` // "pat" | "basic"
	Username      string            `json:"username"`
	SecretRef     string            `json:"-"`
	HasSecret     bool              `json:"has_secret"`
	Options       map[string]string `json:"options"` // e.g. inspection_statuses, author_aliases, default_project
	Enabled       bool              `json:"enabled"`
	LastCheckedAt string            `json:"last_checked_at"`
	LastError     string            `json:"last_error"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type RemoteLink struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	ConnectionID string    `json:"connection_id"`
	RemoteKind   string    `json:"remote_kind"` // "issue" | "merge_request"
	RemoteKey    string    `json:"remote_key"`
	Title        string    `json:"title"`
	RemoteState  string    `json:"remote_state"`
	WebURL       string    `json:"web_url"`
	SyncedAt     time.Time `json:"synced_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// IssueFollowup records local-only tracking state (paused / pending_retest / verified)
// without writing back to external Jira.
type IssueFollowup struct {
	ID           string    `json:"id"`
	ConnectionID string    `json:"connection_id"`
	IssueKey     string    `json:"issue_key"`
	Title        string    `json:"title"`
	WebURL       string    `json:"web_url"`
	RemoteState  string    `json:"remote_state"`
	LocalState   string    `json:"local_state"` // "active" | "paused" | "pending_retest" | "verified"
	Reason       string    `json:"reason"`
	Revision     string    `json:"revision"`
	UpdatedAt    time.Time `json:"updated_at"`
	CreatedAt    time.Time `json:"created_at"`
}

type KnowledgeBase struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	SourceType    string    `json:"source_type"` // "local_dir" | "remote_git"
	Location      string    `json:"location"`
	Branch        string    `json:"branch"`
	SecretRef     string    `json:"-"`
	HasSecret     bool      `json:"has_secret"`
	ReadOnly      bool      `json:"readonly"`
	MirrorDir     string    `json:"mirror_dir,omitempty"`
	LastCommitSHA string    `json:"last_commit_sha"`
	LastSyncedAt  string    `json:"last_synced_at"`
	LastError     string    `json:"last_error"`
	DocCount      int       `json:"doc_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type KnowledgeDoc struct {
	ID            string    `json:"id"`
	KBID          string    `json:"kb_id"`
	RelPath       string    `json:"rel_path"`
	Title         string    `json:"title"`
	ContentSHA256 string    `json:"content_sha256"`
	SizeBytes     int64     `json:"size_bytes"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type KnowledgeDocContent struct {
	Doc     KnowledgeDoc `json:"doc"`
	Content string       `json:"content"`
}

type KnowledgeSearchHit struct {
	DocID   string `json:"doc_id"`
	KBID    string `json:"kb_id"`
	KBName  string `json:"kb_name"`
	RelPath string `json:"rel_path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

type KnowledgeGraphNode struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Group    string `json:"group"`
	Degree   int    `json:"degree"`
	InLinks  int    `json:"in_links"`
	OutLinks int    `json:"out_links"`
}

type KnowledgeGraphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type KnowledgeGraph struct {
	KBID   string               `json:"kb_id"`
	KBName string               `json:"kb_name"`
	Nodes  []KnowledgeGraphNode `json:"nodes"`
	Edges  []KnowledgeGraphEdge `json:"edges"`
	Groups []string             `json:"groups"`
}
