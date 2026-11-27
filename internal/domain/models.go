// Package domain defines the pure business entities shared across core services,
// storage repositories, HTTP APIs, and pluggable components.
package domain

import "time"

// WorkstationNode represents any client/development host (macOS, Windows, or Linux)
// connected to a Lagrangian instance, avoiding any single-device assumption.
type WorkstationNode struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	OS              string    `json:"os"`   // "linux" | "darwin" | "windows"
	Arch            string    `json:"arch"` // "amd64" | "arm64"
	AgentVersion    string    `json:"agent_version"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	CreatedAt       time.Time `json:"created_at"`
}

type Task struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Status          string     `json:"status"`
	Priority        string     `json:"priority"` // "low" | "medium" | "high" | "urgent"
	NextAction      string     `json:"next_action"`
	Blocker         string     `json:"blocker"`
	ResumeCondition string     `json:"resume_condition"`
	Tags            []string   `json:"tags"`
	DueDate         string     `json:"due_date"` // YYYY-MM-DD or empty
	Archived        bool       `json:"archived"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`

	// Aggregated counts populated on list queries
	ProgressCount   int `json:"progress_count,omitempty"`
	CommitCount     int `json:"commit_count,omitempty"`
	RepoCount       int `json:"repo_count,omitempty"`
	AttachmentCount int `json:"attachment_count,omitempty"`
}

type TaskDetail struct {
	Task        Task            `json:"task"`
	Progress    []ProgressEntry `json:"progress"`
	Attachments []Attachment    `json:"attachments"`
	Repos       []RepoBinding   `json:"repos"`
	Commits     []CommitLink    `json:"commits"`
	RemoteLinks []RemoteLink    `json:"remote_links"`
}

type ProgressEntry struct {
	ID          string       `json:"id"`
	TaskID      string       `json:"task_id"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	OccurredOn  string       `json:"occurred_on"` // YYYY-MM-DD
	Source      string       `json:"source"`      // "manual" | "bot" | "mcp" | "system"
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

type Attachment struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	ProgressID     string    `json:"progress_id,omitempty"`
	Filename       string    `json:"filename"`
	MimeType       string    `json:"mime_type"`
	SizeBytes      int64     `json:"size_bytes"`
	SHA256         string    `json:"sha256"`
	StorageRelPath string    `json:"-"`
	DownloadURL    string    `json:"download_url"`
	CreatedAt      time.Time `json:"created_at"`
}

type RepoBinding struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	WorkstationID string    `json:"workstation_id,omitempty"`
	RepoName      string    `json:"repo_name"`
	RepoPath      string    `json:"repo_path"`
	BranchName    string    `json:"branch_name"`
	RemoteURL     string    `json:"remote_url"`
	CreatedAt     time.Time `json:"created_at"`
}

type CommitLink struct {
	ID           string    `json:"id"`
	TaskID       string    `json:"task_id"`
	RepoName     string    `json:"repo_name"`
	BranchName   string    `json:"branch_name"`
	SHA          string    `json:"sha"`
	Title        string    `json:"title"`
	Author       string    `json:"author"`
	CommittedAt  string    `json:"committed_at"`
	PatchID      string    `json:"patch_id"`
	Source       string    `json:"source"` // "manual" | "auto" | "rebase"
	SupersededBy string    `json:"superseded_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type CommitCandidate struct {
	RepoName     string `json:"repo_name"`
	RepoPath     string `json:"repo_path"`
	BranchName   string `json:"branch_name"`
	SHA          string `json:"sha"`
	Title        string `json:"title"`
	Author       string `json:"author"`
	CommittedAt  string `json:"committed_at"`
	PatchID      string `json:"patch_id"`
	AlreadyBound bool   `json:"already_bound"`
	Superseded   bool   `json:"superseded"`
	BoundTaskID  string `json:"bound_task_id,omitempty"`
}

type ReconcileReplacement struct {
	RepoName   string `json:"repo_name"`
	BranchName string `json:"branch_name"`
	OldSHA     string `json:"old_sha"`
	NewSHA     string `json:"new_sha"`
	Title      string `json:"title"`
	PatchID    string `json:"patch_id"`
}

type ReconcileResult struct {
	ScannedBranches int                    `json:"scanned_branches"`
	Replaced        []ReconcileReplacement `json:"replaced"`
	Warnings        []string               `json:"warnings"`
}

// OrbitNode represents an interactive orbital element rendered in the
// Lagrangian Orbit View (inner ring: tasks, middle ring: repos/branches, outer ring: components/nodes).
type OrbitNode struct {
	ID       string            `json:"id"`
	Ring     int               `json:"ring"` // 1 = inner (tasks), 2 = middle (repos), 3 = outer (components)
	Kind     string            `json:"kind"` // "task" | "repo" | "component"
	Label    string            `json:"label"`
	Subtitle string            `json:"subtitle"`
	Status   string            `json:"status"`
	Color    string            `json:"color"`
	Meta     map[string]string `json:"meta,omitempty"`
}

type OrbitSnapshot struct {
	CenterLabel string      `json:"center_label"`
	Mode        string      `json:"mode"`
	HostOS      string      `json:"host_os"`
	Nodes       []OrbitNode `json:"nodes"`
	Stats       OrbitStats  `json:"stats"`
}

type OrbitStats struct {
	TotalTasks            int `json:"total_tasks"`
	ActiveTasks           int `json:"active_tasks"`
	BlockedTasks          int `json:"blocked_tasks"`
	BoundRepos            int `json:"bound_repos"`
	ActiveCommits         int `json:"active_commits"`
	ConnectedWorkstations int `json:"connected_workstations"`
	EnabledComponents     int `json:"enabled_components"`
	TotalComponents       int `json:"total_components"`
}
