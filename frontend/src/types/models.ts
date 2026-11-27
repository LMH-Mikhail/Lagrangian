export interface StatusSpec {
  key: string
  label: string
  category: 'todo' | 'active' | 'blocked' | 'done' | string
  color: string
}

export interface ComponentSlot {
  id: string
  name: string
  driver: string
  enabled: boolean
  secret_ref?: string
  options?: Record<string, string>
}

export interface AppConfig {
  version: number
  server: {
    mode: 'standalone' | 'server'
    host?: string
    port: number
    auth_enabled: boolean
    api_token_ref: string
  }
  storage: {
    data_dir: string
    db_path: string
    attachments_dir: string
  }
  workflow: {
    task_statuses: StatusSpec[]
  }
  git: {
    author_aliases: string[]
    auto_reconcile_rebase: boolean
  }
  components: {
    code_hosts: ComponentSlot[]
    issue_trackers: ComponentSlot[]
    bots: ComponentSlot[]
    agent_sources: ComponentSlot[]
    llm_providers: ComponentSlot[]
    knowledge_bases: ComponentSlot[]
    mcp: {
      enabled: boolean
      transport: string
      allow_write: boolean
    }
  }
}

export interface ComponentStatus {
  id: string
  kind: string
  driver: string
  name: string
  enabled: boolean
  state: 'ready' | 'degraded' | 'disabled' | 'planned'
  detail: string
  checked_at: string
}

export interface Task {
  id: string
  title: string
  description: string
  status: string
  priority: 'low' | 'medium' | 'high' | 'urgent'
  next_action: string
  blocker: string
  resume_condition: string
  tags: string[]
  due_date: string
  archived: boolean
  created_at: string
  updated_at: string
  completed_at?: string
  progress_count?: number
  commit_count?: number
  repo_count?: number
  attachment_count?: number
}

export interface Attachment {
  id: string
  task_id: string
  progress_id?: string
  filename: string
  mime_type: string
  size_bytes: number
  sha256: string
  download_url: string
  created_at: string
}

export interface ProgressEntry {
  id: string
  task_id: string
  title: string
  body: string
  occurred_on: string
  source: string
  created_at: string
  updated_at: string
  attachments?: Attachment[]
}

export interface RepoBinding {
  id: string
  task_id: string
  workstation_id?: string
  repo_name: string
  repo_path: string
  branch_name: string
  remote_url: string
  created_at: string
}

export interface CommitLink {
  id: string
  task_id: string
  repo_name: string
  branch_name: string
  sha: string
  title: string
  author: string
  committed_at: string
  patch_id: string
  source: string
  superseded_by?: string
  created_at: string
}

export interface CommitCandidate {
  repo_name: string
  repo_path: string
  branch_name: string
  sha: string
  title: string
  author: string
  committed_at: string
  patch_id: string
  already_bound: boolean
  superseded: boolean
  bound_task_id?: string
}

export interface TaskDetail {
  task: Task
  progress: ProgressEntry[]
  attachments: Attachment[]
  repos: RepoBinding[]
  commits: CommitLink[]
  remote_links: RemoteLink[]
}

export interface IntegrationConnection {
  id: string
  kind: 'code_host' | 'issue_tracker' | string
  driver: 'gitlab' | 'github' | 'jira' | string
  name: string
  base_url: string
  auth_type: 'pat' | 'basic' | 'oauth_password' | string
  username: string
  has_secret: boolean
  options: Record<string, string>
  enabled: boolean
  last_checked_at: string
  last_error: string
  created_at: string
  updated_at: string
}

export interface ConnectionTestResult {
  ok: boolean
  identity?: string
  error?: string
  checked_at: string
  statuses?: string[]
}

export interface GitLabProject {
  id: number
  name: string
  path_with_namespace: string
  default_branch: string
  web_url: string
}

export interface GitLabMR {
  id: string
  title: string
  state: string
  source_branch: string
  target_branch: string
  web_url: string
}

export interface GitLabRemoteCommit {
  sha: string
  title: string
  author: string
  committed_at: string
  web_url: string
}

export interface JiraIssueWithFollowup {
  key: string
  title: string
  status: string
  assignee: string
  web_url: string
  local_state: 'active' | 'paused' | 'pending_retest' | 'verified' | string
  reason: string
}

export interface IssueFollowup {
  id: string
  connection_id: string
  issue_key: string
  title: string
  web_url: string
  remote_state: string
  local_state: 'active' | 'paused' | 'pending_retest' | 'verified' | string
  reason: string
  revision: string
  updated_at: string
  created_at: string
}

export interface RemoteLink {
  id: string
  task_id: string
  connection_id: string
  remote_kind: 'issue' | 'merge_request' | string
  remote_key: string
  title: string
  remote_state: string
  web_url: string
  synced_at: string
  created_at: string
}

export interface KnowledgeBase {
  id: string
  name: string
  source_type: 'local_dir' | 'remote_git' | string
  location: string
  branch: string
  has_secret: boolean
  readonly: boolean
  mirror_dir?: string
  last_commit_sha: string
  last_synced_at: string
  last_error: string
  doc_count: number
  created_at: string
  updated_at: string
}

export interface KnowledgeDoc {
  id: string
  kb_id: string
  rel_path: string
  title: string
  content_sha256: string
  size_bytes: number
  updated_at: string
}

export interface KnowledgeDocContent {
  doc: KnowledgeDoc
  content: string
}

export interface KnowledgeSearchHit {
  doc_id: string
  kb_id: string
  kb_name: string
  rel_path: string
  title: string
  snippet: string
}

export interface KnowledgeGraphNode {
  id: string
  title: string
  group: string
  degree: number
  in_links: number
  out_links: number
}

export interface KnowledgeGraphEdge {
  source: string
  target: string
}

export interface KnowledgeGraph {
  kb_id: string
  kb_name: string
  nodes: KnowledgeGraphNode[]
  edges: KnowledgeGraphEdge[]
  groups: string[]
}

export interface OrbitNode {
  id: string
  ring: 1 | 2 | 3
  kind: 'task' | 'repo' | 'component'
  label: string
  subtitle: string
  status: string
  color: string
  meta?: Record<string, string>
}

export interface OrbitSnapshot {
  center_label: string
  mode: string
  host_os: string
  nodes: OrbitNode[]
  stats: {
    total_tasks: number
    active_tasks: number
    blocked_tasks: number
    bound_repos: number
    active_commits: number
    connected_workstations: number
    enabled_components: number
    total_components: number
  }
}

export interface ReconcileResult {
  scanned_branches: number
  replaced: Array<{
    repo_name: string
    branch_name: string
    old_sha: string
    new_sha: string
    title: string
    patch_id: string
  }>
  warnings: string[]
}
