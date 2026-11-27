-- 001_init.sql: Core ledger schema for Lagrangian Phase 1
-- Device-agnostic: supports 1..N workstation nodes (Linux/macOS/Windows) and any always-on server OS.

CREATE TABLE IF NOT EXISTS workstation_nodes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    agent_version TEXT NOT NULL DEFAULT '',
    last_heartbeat_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'backlog',
    priority TEXT NOT NULL DEFAULT 'medium',
    next_action TEXT NOT NULL DEFAULT '',
    blocker TEXT NOT NULL DEFAULT '',
    resume_condition TEXT NOT NULL DEFAULT '',
    tags_json TEXT NOT NULL DEFAULT '[]',
    due_date TEXT NOT NULL DEFAULT '',
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_tasks_status_archived ON tasks(status, archived);

CREATE TABLE IF NOT EXISTS task_progress (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    occurred_on TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'manual',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_progress_task ON task_progress(task_id, occurred_on DESC, created_at DESC);

CREATE TABLE IF NOT EXISTS attachments (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    progress_id TEXT REFERENCES task_progress(id) ON DELETE SET NULL,
    filename TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    storage_rel_path TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attachments_task ON attachments(task_id, created_at DESC);

CREATE TABLE IF NOT EXISTS repo_bindings (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    workstation_id TEXT NOT NULL DEFAULT '',
    repo_name TEXT NOT NULL,
    repo_path TEXT NOT NULL DEFAULT '',
    branch_name TEXT NOT NULL,
    remote_url TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(task_id, repo_name, branch_name)
);

CREATE INDEX IF NOT EXISTS idx_repo_bindings_task ON repo_bindings(task_id);

CREATE TABLE IF NOT EXISTS commit_links (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    repo_name TEXT NOT NULL,
    branch_name TEXT NOT NULL DEFAULT '',
    sha TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    committed_at TEXT NOT NULL DEFAULT '',
    patch_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'manual',
    superseded_by TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE(repo_name, sha)
);

CREATE INDEX IF NOT EXISTS idx_commit_links_task ON commit_links(task_id, superseded_by);
CREATE INDEX IF NOT EXISTS idx_commit_links_patch ON commit_links(repo_name, patch_id);
