import type {
  AppConfig,
  Attachment,
  CommitCandidate,
  CommitLink,
  ComponentStatus,
  ConnectionTestResult,
  GitLabMR,
  GitLabProject,
  GitLabRemoteCommit,
  IntegrationConnection,
  IssueFollowup,
  JiraIssueWithFollowup,
  KnowledgeBase,
  KnowledgeDoc,
  KnowledgeDocContent,
  KnowledgeGraph,
  KnowledgeSearchHit,
  OrbitSnapshot,
  ProgressEntry,
  ReconcileResult,
  RemoteLink,
  RepoBinding,
  Task,
  TaskDetail,
} from '../types/models'

const TOKEN_STORAGE_KEY = 'lagrangian_api_token'

export class APIError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

export function getSavedToken(): string {
  return localStorage.getItem(TOKEN_STORAGE_KEY) || ''
}

export function saveToken(token: string): void {
  if (token.trim()) {
    localStorage.setItem(TOKEN_STORAGE_KEY, token.trim())
  } else {
    localStorage.removeItem(TOKEN_STORAGE_KEY)
  }
}

export function attachmentAccessURL(downloadURL: string): string {
  const token = getSavedToken()
  if (!token) return downloadURL
  const sep = downloadURL.includes('?') ? '&' : '?'
  return `${downloadURL}${sep}token=${encodeURIComponent(token)}`
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  const token = getSavedToken()
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }
  if (init.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  const resp = await fetch(path, { ...init, headers })
  const text = await resp.text()
  const payload = text ? JSON.parse(text) : {}
  if (!resp.ok) {
    const errObj = payload?.error
    throw new APIError(
      resp.status,
      errObj?.code || 'request_failed',
      errObj?.message || `请求失败 (${resp.status})`,
    )
  }
  return payload as T
}

export const api = {
  getConfig: () => request<AppConfig>('/api/v1/config'),
  updateConfig: (cfg: AppConfig) =>
    request<AppConfig>('/api/v1/config', {
      method: 'PUT',
      body: JSON.stringify(cfg),
    }),
  updateSystemConfig: (patch: {
    server?: AppConfig['server']
    storage?: AppConfig['storage']
    workflow?: AppConfig['workflow']
  }) =>
    request<AppConfig>('/api/v1/config/system', {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  updateComponentConfig: (
    kind: string,
    body: {
      git?: AppConfig['git']
      slots?: AppConfig['components']['code_hosts']
      mcp?: AppConfig['components']['mcp']
    },
  ) =>
    request<AppConfig>(`/api/v1/config/components/${encodeURIComponent(kind)}`, {
      method: 'PUT',
      body: JSON.stringify(body),
    }),
  listComponents: () => request<{ components: ComponentStatus[] }>('/api/v1/components'),
  orbitSnapshot: () => request<OrbitSnapshot>('/api/v1/system/orbit'),

  listTasks: (params?: { status?: string; q?: string; include_archived?: boolean }) => {
    const qs = new URLSearchParams()
    if (params?.status) qs.set('status', params.status)
    if (params?.q) qs.set('q', params.q)
    if (params?.include_archived) qs.set('include_archived', '1')
    const suffix = qs.toString() ? `?${qs.toString()}` : ''
    return request<{ tasks: Task[] }>(`/api/v1/tasks${suffix}`)
  },
  createTask: (body: Partial<Task>) =>
    request<Task>('/api/v1/tasks', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  getTaskDetail: (id: string) => request<TaskDetail>(`/api/v1/tasks/${id}`),
  updateTask: (id: string, patch: Partial<Task>) =>
    request<Task>(`/api/v1/tasks/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  deleteTask: (id: string) =>
    request<{ deleted: boolean }>(`/api/v1/tasks/${id}`, { method: 'DELETE' }),

  addProgress: (
    taskId: string,
    body: { title: string; body: string; occurred_on: string; source?: string },
  ) =>
    request<ProgressEntry>(`/api/v1/tasks/${taskId}/progress`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  deleteProgress: (taskId: string, progressId: string) =>
    request<{ deleted: boolean }>(`/api/v1/tasks/${taskId}/progress/${progressId}`, {
      method: 'DELETE',
    }),

  uploadAttachment: (taskId: string, file: File, progressId?: string) => {
    const fd = new FormData()
    fd.append('file', file)
    if (progressId) fd.append('progress_id', progressId)
    return request<Attachment>(`/api/v1/tasks/${taskId}/attachments`, {
      method: 'POST',
      body: fd,
    })
  },

  bindRepo: (
    taskId: string,
    body: { repo_name: string; repo_path: string; branch_name: string; remote_url?: string },
  ) =>
    request<RepoBinding>(`/api/v1/tasks/${taskId}/repos`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  unbindRepo: (taskId: string, repoId: string) =>
    request<{ deleted: boolean }>(`/api/v1/tasks/${taskId}/repos/${repoId}`, {
      method: 'DELETE',
    }),

  commitCandidates: (taskId: string, limit = 50) =>
    request<{ candidates: CommitCandidate[]; warnings: string[] }>(
      `/api/v1/tasks/${taskId}/commits/candidates?limit=${limit}`,
    ),
  bindCommits: (
    taskId: string,
    commits: Array<Partial<CommitCandidate> & { source?: string }>,
  ) =>
    request<{ bound: CommitLink[]; skipped: string[] }>(`/api/v1/tasks/${taskId}/commits`, {
      method: 'POST',
      body: JSON.stringify({ commits }),
    }),
  unbindCommit: (taskId: string, commitId: string) =>
    request<{ deleted: boolean }>(`/api/v1/tasks/${taskId}/commits/${commitId}`, {
      method: 'DELETE',
    }),
  reconcileRebases: (taskId: string) =>
    request<ReconcileResult>(`/api/v1/tasks/${taskId}/commits/reconcile`, {
      method: 'POST',
    }),

  // --- Phase 2: External Integrations (GitLab / Jira) ---
  listConnections: (kind?: string) => {
    const suffix = kind ? `?kind=${encodeURIComponent(kind)}` : ''
    return request<{ connections: IntegrationConnection[] }>(
      `/api/v1/integrations/connections${suffix}`,
    )
  },
  createConnection: (body: {
    kind: string
    driver: string
    name: string
    base_url: string
    auth_type: string
    username?: string
    secret?: string
    options?: Record<string, string>
    enabled?: boolean
  }) =>
    request<IntegrationConnection>('/api/v1/integrations/connections', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  updateConnection: (
    id: string,
    patch: {
      name?: string
      base_url?: string
      auth_type?: string
      username?: string
      secret?: string
      options?: Record<string, string>
      enabled?: boolean
    },
  ) =>
    request<IntegrationConnection>(`/api/v1/integrations/connections/${encodeURIComponent(id)}`, {
      method: 'PATCH',
      body: JSON.stringify(patch),
    }),
  deleteConnection: (id: string) =>
    request<{ deleted: boolean }>(`/api/v1/integrations/connections/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  testConnectionDraft: (body: {
    connection_id?: string
    kind: string
    driver: string
    name?: string
    base_url: string
    auth_type: string
    username?: string
    secret?: string
  }) =>
    request<ConnectionTestResult>('/api/v1/integrations/connections/test', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  testConnection: (id: string) =>
    request<ConnectionTestResult>(
      `/api/v1/integrations/connections/${encodeURIComponent(id)}/test`,
      { method: 'POST' },
    ),

  gitlabProjects: (connId: string, q?: string) => {
    const suffix = q ? `?q=${encodeURIComponent(q)}` : ''
    return request<{ projects: GitLabProject[] }>(
      `/api/v1/integrations/connections/${encodeURIComponent(connId)}/codehost/projects${suffix}`,
    )
  },
  gitlabMRs: (
    connId: string,
    params?: { project?: string; scope?: string; state?: string; q?: string },
  ) => {
    const qs = new URLSearchParams()
    if (params?.project) qs.set('project', params.project)
    if (params?.scope) qs.set('scope', params.scope)
    if (params?.state) qs.set('state', params.state)
    if (params?.q) qs.set('q', params.q)
    const suffix = qs.toString() ? `?${qs.toString()}` : ''
    return request<{ merge_requests: GitLabMR[] }>(
      `/api/v1/integrations/connections/${encodeURIComponent(connId)}/codehost/mrs${suffix}`,
    )
  },
  gitlabCommits: (connId: string, project: string, branch: string) => {
    const qs = new URLSearchParams({ project, branch })
    return request<{ commits: GitLabRemoteCommit[] }>(
      `/api/v1/integrations/connections/${encodeURIComponent(connId)}/codehost/commits?${qs.toString()}`,
    )
  },
  jiraIssues: (
    connId: string,
    params?: { project?: string; statuses?: string; assignee?: string; q?: string },
  ) => {
    const qs = new URLSearchParams()
    if (params?.project) qs.set('project', params.project)
    if (params?.statuses) qs.set('statuses', params.statuses)
    if (params?.assignee) qs.set('assignee', params.assignee)
    if (params?.q) qs.set('q', params.q)
    const suffix = qs.toString() ? `?${qs.toString()}` : ''
    return request<{ issues: JiraIssueWithFollowup[] }>(
      `/api/v1/integrations/connections/${encodeURIComponent(connId)}/jira/issues${suffix}`,
    )
  },

  listFollowups: (params?: { connection_id?: string; local_state?: string }) => {
    const qs = new URLSearchParams()
    if (params?.connection_id) qs.set('connection_id', params.connection_id)
    if (params?.local_state) qs.set('local_state', params.local_state)
    const suffix = qs.toString() ? `?${qs.toString()}` : ''
    return request<{ followups: IssueFollowup[] }>(`/api/v1/integrations/followups${suffix}`)
  },
  setFollowup: (body: {
    connection_id: string
    issue_key: string
    title?: string
    web_url?: string
    remote_state?: string
    local_state: string
    reason?: string
  }) =>
    request<IssueFollowup>('/api/v1/integrations/followups', {
      method: 'PUT',
      body: JSON.stringify(body),
    }),

  linkRemote: (
    taskId: string,
    body: {
      connection_id: string
      remote_kind: 'issue' | 'merge_request'
      remote_key: string
      title: string
      remote_state: string
      web_url: string
    },
  ) =>
    request<RemoteLink>(`/api/v1/tasks/${encodeURIComponent(taskId)}/remote-links`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  unlinkRemote: (taskId: string, linkId: string) =>
    request<{ deleted: boolean }>(
      `/api/v1/tasks/${encodeURIComponent(taskId)}/remote-links/${encodeURIComponent(linkId)}`,
      { method: 'DELETE' },
    ),

  // --- Phase 2: Generic Multi-Knowledge-Base & FTS5 Search ---
  listKnowledgeBases: () =>
    request<{ knowledge_bases: KnowledgeBase[] }>('/api/v1/knowledge/bases'),
  createKnowledgeBase: (body: {
    name: string
    source_type: 'local_dir' | 'remote_git'
    location: string
    branch?: string
    secret?: string
    readonly?: boolean
  }) =>
    request<KnowledgeBase>('/api/v1/knowledge/bases', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  deleteKnowledgeBase: (id: string) =>
    request<{ deleted: boolean }>(`/api/v1/knowledge/bases/${encodeURIComponent(id)}`, {
      method: 'DELETE',
    }),
  syncKnowledgeBase: (id: string) =>
    request<KnowledgeBase>(`/api/v1/knowledge/bases/${encodeURIComponent(id)}/sync`, {
      method: 'POST',
    }),
  listKnowledgeDocs: (kbId: string) =>
    request<{ docs: KnowledgeDoc[] }>(`/api/v1/knowledge/bases/${encodeURIComponent(kbId)}/docs`),
  getKnowledgeGraph: (kbId: string) =>
    request<KnowledgeGraph>(`/api/v1/knowledge/bases/${encodeURIComponent(kbId)}/graph`),
  readKnowledgeDoc: (kbId: string, path: string) =>
    request<KnowledgeDocContent>(
      `/api/v1/knowledge/bases/${encodeURIComponent(kbId)}/doc?path=${encodeURIComponent(path)}`,
    ),
  saveKnowledgeDoc: (kbId: string, path: string, content: string) =>
    request<KnowledgeDocContent>(`/api/v1/knowledge/bases/${encodeURIComponent(kbId)}/doc`, {
      method: 'PUT',
      body: JSON.stringify({ path, content }),
    }),
  searchKnowledge: (q: string, kbId?: string, limit = 30) => {
    const qs = new URLSearchParams({ q, limit: String(limit) })
    if (kbId) qs.set('kb_id', kbId)
    return request<{ hits: KnowledgeSearchHit[] }>(`/api/v1/knowledge/search?${qs.toString()}`)
  },
}
