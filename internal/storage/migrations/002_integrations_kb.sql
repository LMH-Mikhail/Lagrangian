-- 002_integrations_kb.sql: Phase 2 external integrations (GitLab/Jira) and generic multi-knowledge-base schema

CREATE TABLE IF NOT EXISTS integration_connections (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,          -- "code_host" | "issue_tracker"
    driver TEXT NOT NULL,        -- "gitlab" | "jira"
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    auth_type TEXT NOT NULL,     -- "pat" | "basic"
    username TEXT NOT NULL DEFAULT '',
    secret_ref TEXT NOT NULL DEFAULT '',
    options_json TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_checked_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_integration_connections_kind ON integration_connections(kind, enabled);

CREATE TABLE IF NOT EXISTS remote_links (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL REFERENCES integration_connections(id) ON DELETE CASCADE,
    remote_kind TEXT NOT NULL,   -- "issue" | "merge_request"
    remote_key TEXT NOT NULL,    -- e.g. "PROJ-123" or "group/repo!42"
    title TEXT NOT NULL DEFAULT '',
    remote_state TEXT NOT NULL DEFAULT '',
    web_url TEXT NOT NULL DEFAULT '',
    synced_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(task_id, connection_id, remote_kind, remote_key)
);

CREATE INDEX IF NOT EXISTS idx_remote_links_task ON remote_links(task_id);

CREATE TABLE IF NOT EXISTS issue_followups (
    id TEXT PRIMARY KEY,
    connection_id TEXT NOT NULL REFERENCES integration_connections(id) ON DELETE CASCADE,
    issue_key TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    web_url TEXT NOT NULL DEFAULT '',
    remote_state TEXT NOT NULL DEFAULT '',
    local_state TEXT NOT NULL DEFAULT 'active', -- "active" | "paused" | "pending_retest" | "verified"
    reason TEXT NOT NULL DEFAULT '',
    revision TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(connection_id, issue_key)
);

CREATE INDEX IF NOT EXISTS idx_issue_followups_conn ON issue_followups(connection_id, local_state);

CREATE TABLE IF NOT EXISTS knowledge_bases (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_type TEXT NOT NULL,   -- "local_dir" | "remote_git"
    location TEXT NOT NULL,      -- local absolute path or remote git URL
    branch TEXT NOT NULL DEFAULT '',
    secret_ref TEXT NOT NULL DEFAULT '',
    readonly INTEGER NOT NULL DEFAULT 0,
    mirror_dir TEXT NOT NULL DEFAULT '',
    last_commit_sha TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS knowledge_docs (
    id TEXT PRIMARY KEY,
    kb_id TEXT NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    rel_path TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    content_sha256 TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    UNIQUE(kb_id, rel_path)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_docs_kb ON knowledge_docs(kb_id, rel_path);

CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(
    doc_id UNINDEXED,
    kb_id UNINDEXED,
    rel_path,
    title,
    body
);
