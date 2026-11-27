<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api } from '../api/client'
import type {
  AppConfig,
  ComponentStatus,
  IntegrationConnection,
  KnowledgeBase,
} from '../types/models'

const props = defineProps<{
  config: AppConfig
}>()

const emit = defineEmits<{
  (e: 'updated', cfg: AppConfig): void
  (e: 'changed'): void
}>()

type SectionKey =
  | 'system'
  | 'code_host'
  | 'issue_tracker'
  | 'knowledge_base'
  | 'agent_source'
  | 'mcp_gateway'
  | 'llm_provider'
  | 'bot_channel'

interface SectionMeta {
  key: SectionKey
  title: string
  phase: string
  contract: string
  description: string
}

const sections: SectionMeta[] = [
  {
    key: 'system',
    title: '基础系统',
    phase: 'Phase 1',
    contract: 'core.Config',
    description: '控制运行模式、网络监听与 SQLite/附件存储目录，不包含任何外部组件参数。',
  },
  {
    key: 'code_host',
    title: 'Git 与代码托管',
    phase: 'Phase 1 + 2',
    contract: 'components.CodeHost',
    description: '统一管理本地 Git 提交作者筛选、patch-id Rebase 自动归并，以及多实例远端 GitLab / GitHub 连接。',
  },
  {
    key: 'issue_tracker',
    title: '工单系统 (Jira)',
    phase: 'Phase 2',
    contract: 'components.IssueTracker',
    description: '多实例 Jira REST API v2 连接管理、可配置巡检状态白名单与本地搁置/验收跟踪。',
  },
  {
    key: 'knowledge_base',
    title: '通用知识库',
    phase: 'Phase 2',
    contract: 'components.KnowledgeSource',
    description: '自定义任意数量本地目录或远端 Git 知识库，支持同步镜像与 SQLite FTS5 全文检索。',
  },
  {
    key: 'agent_source',
    title: 'Agent 对话同步',
    phase: 'Phase 3',
    contract: 'components.AgentSource',
    description: '支持本机直读与跨工作节点（Linux/macOS/Windows）同步 Codex / Antigravity 对话索引与证据（将在 Phase 3 激活）。',
  },
  {
    key: 'mcp_gateway',
    title: '通用 MCP 网关',
    phase: 'Phase 3',
    contract: 'transport.MCPServer',
    description: '支持本地 stdio 与跨主机 Streamable HTTP/SSE 接入任意兼容 MCP 的 Agent 客户端（将在 Phase 3 激活）。',
  },
  {
    key: 'llm_provider',
    title: 'LLM 模型路由',
    phase: 'Phase 4',
    contract: 'components.LLMProvider',
    description: '统一接入本地模型（Ollama / CLI）与远端模型（OpenAI 兼容 API），按场景路由（将在 Phase 4 激活）。',
  },
  {
    key: 'bot_channel',
    title: '多机器人通道',
    phase: 'Phase 4',
    contract: 'components.BotChannel',
    description: '支持绑定多个企业微信等机器人实例并按规则分配巡检、任务、汇报职责（将在 Phase 4 激活）。',
  },
]

function cloneConfig(cfg: AppConfig): AppConfig {
  return JSON.parse(JSON.stringify(cfg)) as AppConfig
}

const activeSection = ref<SectionKey>('system')
const draft = ref<AppConfig>(cloneConfig(props.config))
const aliasesText = ref((props.config.git?.author_aliases || []).join(', '))
const components = ref<ComponentStatus[]>([])
const connections = ref<IntegrationConnection[]>([])
const knowledgeBases = ref<KnowledgeBase[]>([])
const saving = ref(false)
const testingDraft = ref(false)
const busyId = ref('')
const msg = ref('')
const errMsg = ref('')

// New CodeHost (GitLab / GitHub) connection form
const gitlabForm = reactive({
  driver: 'gitlab' as 'gitlab' | 'github',
  name: '',
  base_url: '',
  auth_type: 'pat' as 'pat' | 'oauth_password',
  username: '',
  secret: '',
})
const gitlabDraftVerified = reactive({
  ok: false,
  identity: '',
  fingerprint: '',
})

function onCodeHostDriverChange() {
  gitlabDraftVerified.ok = false
  if (gitlabForm.driver === 'github') {
    gitlabForm.auth_type = 'pat'
    if (!gitlabForm.base_url.trim()) {
      gitlabForm.base_url = 'https://api.github.com'
    }
  } else if (gitlabForm.base_url.trim() === 'https://api.github.com') {
    gitlabForm.base_url = ''
  }
}

// New Jira connection form
const jiraForm = reactive({
  name: '',
  base_url: '',
  auth_type: 'pat' as 'pat' | 'basic',
  username: '',
  secret: '',
  default_project: '',
  inspection_statuses: '',
})
const jiraDraftVerified = reactive({
  ok: false,
  identity: '',
  fingerprint: '',
  statuses: [] as string[],
})

const gitlabFingerprint = computed(() =>
  [gitlabForm.driver, gitlabForm.base_url.trim(), gitlabForm.auth_type, gitlabForm.username.trim(), gitlabForm.secret.trim()].join('|'),
)
const isGitLabDraftReady = computed(
  () => gitlabDraftVerified.ok && gitlabDraftVerified.fingerprint === gitlabFingerprint.value,
)

const jiraFingerprint = computed(() =>
  [jiraForm.base_url.trim(), jiraForm.auth_type, jiraForm.username.trim(), jiraForm.secret.trim()].join('|'),
)
const isJiraDraftReady = computed(
  () => jiraDraftVerified.ok && jiraDraftVerified.fingerprint === jiraFingerprint.value,
)

const selectedJiraStatuses = computed(() =>
  jiraForm.inspection_statuses
    .split(/[,，\n]/)
    .map((s) => s.trim())
    .filter(Boolean),
)

// Edit existing connection state (shared for GitLab & Jira)
const editingConnId = ref('')
const loadingEditStatuses = ref(false)
const editForm = reactive({
  id: '',
  kind: '',
  driver: '',
  name: '',
  base_url: '',
  auth_type: 'pat',
  username: '',
  secret: '',
  author_aliases: '',
  default_project: '',
  inspection_statuses: '',
  origFingerprint: '',
  origHasError: false,
})
const editVerified = reactive({
  ok: false,
  identity: '',
  fingerprint: '',
  statuses: [] as string[],
})

const editFingerprint = computed(() =>
  [editForm.base_url.trim(), editForm.auth_type, editForm.username.trim(), editForm.secret.trim()].join('|'),
)
const editNeedsTest = computed(
  () => editForm.origHasError || editFingerprint.value !== editForm.origFingerprint,
)
const isEditReadyToSave = computed(
  () => !editNeedsTest.value || (editVerified.ok && editVerified.fingerprint === editFingerprint.value),
)
const editSelectedJiraStatuses = computed(() =>
  editForm.inspection_statuses
    .split(/[,，\n]/)
    .map((s) => s.trim())
    .filter(Boolean),
)

// New KnowledgeBase form
const kbForm = reactive({
  name: '',
  source_type: 'local_dir' as 'local_dir' | 'remote_git',
  location: '',
  branch: 'main',
  secret: '',
  readonly: false,
})

const currentMeta = computed(
  () => sections.find((s) => s.key === activeSection.value) || sections[0],
)

const gitlabConnections = computed(() =>
  connections.value.filter((c) => c.kind === 'code_host'),
)

const jiraConnections = computed(() =>
  connections.value.filter((c) => c.kind === 'issue_tracker'),
)

const statusByKind = computed(() => {
  const rank: Record<string, number> = {
    degraded: 4,
    ready: 3,
    planned: 2,
    disabled: 1,
  }
  const m = new Map<string, ComponentStatus>()
  for (const c of components.value) {
    const existing = m.get(c.kind)
    if (!existing || (rank[c.state] || 0) > (rank[existing.state] || 0)) {
      m.set(c.kind, c)
    }
  }
  return m
})

onMounted(loadAll)

async function loadAll() {
  await Promise.all([loadComponentStatuses(), loadConnections(), loadKnowledgeBases()])
}

async function loadComponentStatuses() {
  try {
    const res = await api.listComponents()
    components.value = res.components
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载组件状态失败'
  }
}

async function loadConnections() {
  try {
    const res = await api.listConnections()
    connections.value = res.connections
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载外部连接列表失败'
  }
}

async function loadKnowledgeBases() {
  try {
    const res = await api.listKnowledgeBases()
    knowledgeBases.value = res.knowledge_bases
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载知识库列表失败'
  }
}

function selectSection(key: SectionKey) {
  activeSection.value = key
  msg.value = ''
  errMsg.value = ''
}

// --- Phase 1: Base System & Local Git ---

async function saveSystem() {
  saving.value = true
  msg.value = ''
  errMsg.value = ''
  try {
    const updated = await api.updateSystemConfig({
      server: {
        ...draft.value.server,
        host: '',
      },
      storage: draft.value.storage,
      workflow: draft.value.workflow,
    })
    draft.value = cloneConfig(updated)
    emit('updated', updated)
    await loadComponentStatuses()
    msg.value = '基础系统与任务状态流配置已保存。'
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '保存基础系统配置失败'
  } finally {
    saving.value = false
  }
}

function addStatusRow() {
  const idx = draft.value.workflow.task_statuses.length + 1
  draft.value.workflow.task_statuses.push({
    key: `custom_${idx}`,
    label: `自定义状态 ${idx}`,
    category: 'active',
    color: '#38bdf8',
  })
}

function removeStatusRow(index: number) {
  if (draft.value.workflow.task_statuses.length <= 1) return
  draft.value.workflow.task_statuses.splice(index, 1)
}

function moveStatusRow(index: number, delta: -1 | 1) {
  const target = index + delta
  const list = draft.value.workflow.task_statuses
  if (target < 0 || target >= list.length) return
  const [item] = list.splice(index, 1)
  list.splice(target, 0, item)
}

function resetDefaultStatuses() {
  draft.value.workflow.task_statuses = [
    { key: 'backlog', label: '待规划', category: 'todo', color: '#8b949e' },
    { key: 'ready', label: '就绪', category: 'todo', color: '#58a6ff' },
    { key: 'in_progress', label: '进行中', category: 'active', color: '#3fb950' },
    { key: 'blocked', label: '阻塞中', category: 'blocked', color: '#f85149' },
    { key: 'in_review', label: '待验收', category: 'active', color: '#d29922' },
    { key: 'done', label: '已完成', category: 'done', color: '#a371f7' },
    { key: 'canceled', label: '已取消', category: 'done', color: '#6e7681' },
  ]
}

async function saveCodeHostComponent() {
  saving.value = true
  msg.value = ''
  errMsg.value = ''
  try {
    const aliases = aliasesText.value
      .split(/[,，\n]/)
      .map((s) => s.trim())
      .filter(Boolean)
    const updated = await api.updateComponentConfig('code_host', {
      git: {
        author_aliases: aliases,
        auto_reconcile_rebase: draft.value.git.auto_reconcile_rebase,
      },
    })
    draft.value = cloneConfig(updated)
    aliasesText.value = (updated.git?.author_aliases || []).join(', ')
    emit('updated', updated)
    await loadComponentStatuses()
    msg.value = '本地 Git 提交与 Rebase 归并规则已保存并热生效。'
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '保存 Git 组件配置失败'
  } finally {
    saving.value = false
  }
}

// --- Phase 2: External Connections (GitLab & Jira) ---

async function testGitLabDraft() {
  const effectiveURL =
    gitlabForm.base_url.trim() || (gitlabForm.driver === 'github' ? 'https://api.github.com' : '')
  if (!effectiveURL) {
    errMsg.value = '请先填写代码托管服务地址。'
    return
  }
  testingDraft.value = true
  msg.value = ''
  errMsg.value = ''
  gitlabDraftVerified.ok = false
  const label = gitlabForm.driver === 'github' ? 'GitHub' : 'GitLab'
  try {
    const res = await api.testConnectionDraft({
      kind: 'code_host',
      driver: gitlabForm.driver,
      name: gitlabForm.name.trim(),
      base_url: effectiveURL,
      auth_type: gitlabForm.driver === 'github' ? 'pat' : gitlabForm.auth_type,
      username: gitlabForm.username.trim(),
      secret: gitlabForm.secret.trim(),
    })
    if (res.ok) {
      gitlabDraftVerified.ok = true
      gitlabDraftVerified.identity = res.identity || '已验证'
      gitlabDraftVerified.fingerprint = gitlabFingerprint.value
      msg.value = `${label} 连通性测试通过（身份：${gitlabDraftVerified.identity}），现在可以点击「添加连接」保存。`
    } else {
      errMsg.value = `${label} 连通性测试失败：${res.error || '请核对地址与凭据'}`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : `${label} 连通性测试失败`
  } finally {
    testingDraft.value = false
  }
}

async function addGitLabConnection() {
  const effectiveURL =
    gitlabForm.base_url.trim() || (gitlabForm.driver === 'github' ? 'https://api.github.com' : '')
  if (!gitlabForm.name.trim() || !effectiveURL) {
    errMsg.value = '请填写连接名称与服务地址。'
    return
  }
  if (!isGitLabDraftReady.value) {
    errMsg.value = '请先点击「测试连通性」验证通过后再保存。'
    return
  }
  saving.value = true
  msg.value = ''
  errMsg.value = ''
  const label = gitlabForm.driver === 'github' ? 'GitHub' : 'GitLab'
  try {
    await api.createConnection({
      kind: 'code_host',
      driver: gitlabForm.driver,
      name: gitlabForm.name.trim(),
      base_url: effectiveURL,
      auth_type: gitlabForm.driver === 'github' ? 'pat' : gitlabForm.auth_type,
      username: gitlabForm.username.trim(),
      secret: gitlabForm.secret.trim(),
      options: {},
      enabled: true,
    })
    gitlabForm.name = ''
    gitlabForm.base_url = gitlabForm.driver === 'github' ? 'https://api.github.com' : ''
    gitlabForm.username = ''
    gitlabForm.secret = ''
    gitlabDraftVerified.ok = false
    gitlabDraftVerified.identity = ''
    gitlabDraftVerified.fingerprint = ''
    await Promise.all([loadConnections(), loadComponentStatuses()])
    emit('changed')
    msg.value = `${label} 连接已验证并保存，凭据已写入安全存储。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : `创建 ${label} 连接失败`
  } finally {
    saving.value = false
  }
}

async function testJiraDraft() {
  if (!jiraForm.base_url.trim()) {
    errMsg.value = '请先填写 Jira 服务地址。'
    return
  }
  testingDraft.value = true
  msg.value = ''
  errMsg.value = ''
  jiraDraftVerified.ok = false
  jiraDraftVerified.statuses = []
  try {
    const res = await api.testConnectionDraft({
      kind: 'issue_tracker',
      driver: 'jira',
      name: jiraForm.name.trim(),
      base_url: jiraForm.base_url.trim(),
      auth_type: jiraForm.auth_type,
      username: jiraForm.username.trim(),
      secret: jiraForm.secret.trim(),
    })
    if (res.ok) {
      jiraDraftVerified.ok = true
      jiraDraftVerified.identity = res.identity || '已验证'
      jiraDraftVerified.fingerprint = jiraFingerprint.value
      jiraDraftVerified.statuses = res.statuses || []
      msg.value =
        `Jira 连通性测试通过（身份：${jiraDraftVerified.identity}）` +
        (jiraDraftVerified.statuses.length
          ? `，已读取该实例 ${jiraDraftVerified.statuses.length} 个工单状态供点选。`
          : '，现在可以点击「添加 Jira 连接」保存。')
    } else {
      errMsg.value = `Jira 连通性测试失败：${res.error || '请核对地址与凭据'}`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : 'Jira 连通性测试失败'
  } finally {
    testingDraft.value = false
  }
}

function toggleJiraStatusChip(statusName: string) {
  const list = [...selectedJiraStatuses.value]
  const idx = list.indexOf(statusName)
  if (idx >= 0) {
    list.splice(idx, 1)
  } else {
    list.push(statusName)
  }
  jiraForm.inspection_statuses = list.join(', ')
}

async function addJiraConnection() {
  if (!jiraForm.name.trim() || !jiraForm.base_url.trim()) {
    errMsg.value = '请填写 Jira 连接名称与服务地址。'
    return
  }
  if (!isJiraDraftReady.value) {
    errMsg.value = '请先点击「测试连通性」验证通过后再保存。'
    return
  }
  saving.value = true
  msg.value = ''
  errMsg.value = ''
  try {
    const opts: Record<string, string> = {}
    if (jiraForm.default_project.trim()) {
      opts.default_project = jiraForm.default_project.trim()
    }
    if (jiraForm.inspection_statuses.trim()) {
      opts.inspection_statuses = jiraForm.inspection_statuses.trim()
    }
    await api.createConnection({
      kind: 'issue_tracker',
      driver: 'jira',
      name: jiraForm.name.trim(),
      base_url: jiraForm.base_url.trim(),
      auth_type: jiraForm.auth_type,
      username: jiraForm.username.trim(),
      secret: jiraForm.secret.trim(),
      options: opts,
      enabled: true,
    })
    jiraForm.name = ''
    jiraForm.base_url = ''
    jiraForm.username = ''
    jiraForm.secret = ''
    jiraForm.default_project = ''
    jiraForm.inspection_statuses = ''
    jiraDraftVerified.ok = false
    jiraDraftVerified.identity = ''
    jiraDraftVerified.fingerprint = ''
    jiraDraftVerified.statuses = []
    await Promise.all([loadConnections(), loadComponentStatuses()])
    emit('changed')
    msg.value = 'Jira 连接已验证并保存，凭据已写入安全存储。'
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '创建 Jira 连接失败'
  } finally {
    saving.value = false
  }
}

function startEditConn(conn: IntegrationConnection) {
  if (editingConnId.value === conn.id) {
    cancelEditConn()
    return
  }
  editingConnId.value = conn.id
  editForm.id = conn.id
  editForm.kind = conn.kind
  editForm.driver = conn.driver
  editForm.name = conn.name
  editForm.base_url = conn.base_url
  editForm.auth_type = conn.auth_type
  editForm.username = conn.username || ''
  editForm.secret = ''
  editForm.author_aliases = conn.options?.author_aliases || ''
  editForm.default_project = conn.options?.default_project || ''
  editForm.inspection_statuses = conn.options?.inspection_statuses || ''
  editForm.origFingerprint = [conn.base_url.trim(), conn.auth_type, (conn.username || '').trim(), ''].join('|')
  editForm.origHasError = !!conn.last_error
  editVerified.ok = false
  editVerified.identity = ''
  editVerified.fingerprint = ''
  editVerified.statuses = []
  msg.value = ''
  errMsg.value = ''

  if (conn.driver === 'jira' && !conn.last_error) {
    loadEditJiraStatuses(conn)
  }
}

async function loadEditJiraStatuses(conn: IntegrationConnection) {
  loadingEditStatuses.value = true
  try {
    const res = await api.testConnectionDraft({
      connection_id: conn.id,
      kind: conn.kind,
      driver: conn.driver,
      name: conn.name,
      base_url: conn.base_url,
      auth_type: conn.auth_type,
      username: conn.username || '',
      secret: '',
    })
    if (editingConnId.value === conn.id && res.ok && res.statuses?.length) {
      editVerified.statuses = res.statuses
    }
  } catch {
    // Non-fatal: user can still type statuses manually or click 测试连通性
  } finally {
    loadingEditStatuses.value = false
  }
}

function cancelEditConn() {
  editingConnId.value = ''
  editForm.secret = ''
  editVerified.ok = false
  editVerified.statuses = []
}

function toggleEditJiraStatusChip(statusName: string) {
  const list = [...editSelectedJiraStatuses.value]
  const idx = list.indexOf(statusName)
  if (idx >= 0) {
    list.splice(idx, 1)
  } else {
    list.push(statusName)
  }
  editForm.inspection_statuses = list.join(', ')
}

async function testEditConn() {
  if (!editForm.base_url.trim()) {
    errMsg.value = '请填写服务地址。'
    return
  }
  testingDraft.value = true
  msg.value = ''
  errMsg.value = ''
  editVerified.ok = false
  editVerified.statuses = []
  try {
    const res = await api.testConnectionDraft({
      connection_id: editForm.id,
      kind: editForm.kind,
      driver: editForm.driver,
      name: editForm.name.trim(),
      base_url: editForm.base_url.trim(),
      auth_type: editForm.auth_type,
      username: editForm.username.trim(),
      secret: editForm.secret.trim(),
    })
    if (res.ok) {
      editVerified.ok = true
      editVerified.identity = res.identity || '已验证'
      editVerified.fingerprint = editFingerprint.value
      editVerified.statuses = res.statuses || []
      msg.value =
        `连接「${editForm.name || editForm.driver}」连通性测试通过（身份：${editVerified.identity}）` +
        (editVerified.statuses.length
          ? `，已读取 ${editVerified.statuses.length} 个工单状态供点选。`
          : '，现在可以点击「保存修改」。')
    } else {
      errMsg.value = `连通性测试失败：${res.error || '请核对地址与凭据'}`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '连通性测试失败'
  } finally {
    testingDraft.value = false
  }
}

async function saveEditConn() {
  if (!editForm.name.trim() || !editForm.base_url.trim()) {
    errMsg.value = '连接名称与服务地址不能为空。'
    return
  }
  if (!isEditReadyToSave.value) {
    errMsg.value = '地址或凭据已修改（或原连接处于异常状态），请先点击「测试连通性」验证通过后再保存。'
    return
  }
  saving.value = true
  msg.value = ''
  errMsg.value = ''
  try {
    const opts: Record<string, string> = {}
    if (editForm.driver === 'jira') {
      if (editForm.default_project.trim()) {
        opts.default_project = editForm.default_project.trim()
      }
      if (editForm.inspection_statuses.trim()) {
        opts.inspection_statuses = editForm.inspection_statuses.trim()
      }
    }
    const patch: {
      name: string
      base_url: string
      auth_type: string
      username: string
      secret?: string
      options: Record<string, string>
    } = {
      name: editForm.name.trim(),
      base_url: editForm.base_url.trim(),
      auth_type: editForm.auth_type,
      username: editForm.username.trim(),
      options: opts,
    }
    if (editForm.secret.trim()) {
      patch.secret = editForm.secret.trim()
    }
    await api.updateConnection(editForm.id, patch)
    cancelEditConn()
    await Promise.all([loadConnections(), loadComponentStatuses()])
    emit('changed')
    msg.value = '连接配置已更新。'
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '更新连接失败'
  } finally {
    saving.value = false
  }
}

async function testConn(conn: IntegrationConnection) {
  busyId.value = conn.id
  msg.value = ''
  errMsg.value = ''
  try {
    const res = await api.testConnection(conn.id)
    await Promise.all([loadConnections(), loadComponentStatuses()])
    emit('changed')
    if (res.ok) {
      msg.value = `连接「${conn.name}」连通性正常（身份：${res.identity || 'OK'}）。`
    } else {
      errMsg.value = `连接「${conn.name}」检测失败：${res.error || '未知错误'}`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '连通性检测失败'
  } finally {
    busyId.value = ''
  }
}

async function toggleConn(conn: IntegrationConnection) {
  busyId.value = conn.id
  msg.value = ''
  errMsg.value = ''
  try {
    await api.updateConnection(conn.id, { enabled: !conn.enabled })
    await Promise.all([loadConnections(), loadComponentStatuses()])
    emit('changed')
    msg.value = `连接「${conn.name}」已${conn.enabled ? '停用' : '启用'}。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '更新连接状态失败'
  } finally {
    busyId.value = ''
  }
}

async function removeConn(conn: IntegrationConnection) {
  if (!confirm(`确认删除外部连接「${conn.name}」及其安全存储中的凭据？`)) return
  busyId.value = conn.id
  msg.value = ''
  errMsg.value = ''
  try {
    await api.deleteConnection(conn.id)
    await Promise.all([loadConnections(), loadComponentStatuses()])
    emit('changed')
    msg.value = `连接「${conn.name}」已删除。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '删除连接失败'
  } finally {
    busyId.value = ''
  }
}

// --- Phase 2: Knowledge Bases ---

async function addKnowledgeBase() {
  if (!kbForm.name.trim() || !kbForm.location.trim()) {
    errMsg.value = '请填写知识库名称与本地目录路径或远端 Git 地址。'
    return
  }
  saving.value = true
  msg.value = ''
  errMsg.value = ''
  try {
    const created = await api.createKnowledgeBase({
      name: kbForm.name.trim(),
      source_type: kbForm.source_type,
      location: kbForm.location.trim(),
      branch: kbForm.source_type === 'remote_git' ? kbForm.branch.trim() : '',
      secret: kbForm.source_type === 'remote_git' ? kbForm.secret.trim() : '',
      readonly: kbForm.readonly,
    })
    kbForm.name = ''
    kbForm.location = ''
    kbForm.secret = ''
    kbForm.readonly = false
    await Promise.all([loadKnowledgeBases(), loadComponentStatuses()])
    emit('changed')
    if (created.last_error) {
      errMsg.value = `知识库已创建，但首次同步失败：${created.last_error}`
    } else {
      msg.value = `知识库「${created.name}」已创建并完成索引（${created.doc_count} 篇文档）。`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '创建知识库失败'
  } finally {
    saving.value = false
  }
}

async function syncKB(kb: KnowledgeBase) {
  busyId.value = kb.id
  msg.value = ''
  errMsg.value = ''
  try {
    const updated = await api.syncKnowledgeBase(kb.id)
    await Promise.all([loadKnowledgeBases(), loadComponentStatuses()])
    emit('changed')
    if (updated.last_error) {
      errMsg.value = `知识库「${kb.name}」同步失败：${updated.last_error}`
    } else {
      msg.value = `知识库「${kb.name}」同步完成，当前共索引 ${updated.doc_count} 篇文档。`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '同步知识库失败'
  } finally {
    busyId.value = ''
  }
}

async function removeKB(kb: KnowledgeBase) {
  if (!confirm(`确认移除知识库「${kb.name}」？（本地目录源文件不会被删除，远端镜像缓存将被清理）`)) return
  busyId.value = kb.id
  msg.value = ''
  errMsg.value = ''
  try {
    await api.deleteKnowledgeBase(kb.id)
    await Promise.all([loadKnowledgeBases(), loadComponentStatuses()])
    emit('changed')
    msg.value = `知识库「${kb.name}」已移除。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '移除知识库失败'
  } finally {
    busyId.value = ''
  }
}
</script>

<template>
  <div class="settings-layout">
    <!-- Left Independent Navigation -->
    <aside class="card stack nav-col">
      <div class="muted" style="font-size: 11px; letter-spacing: 0.08em">基础与组件配置</div>
      <button
        v-for="sec in sections"
        :key="sec.key"
        class="nav-item"
        :class="{ active: activeSection === sec.key }"
        @click="selectSection(sec.key)"
      >
        <div class="row" style="justify-content: space-between; width: 100%">
          <span>{{ sec.title }}</span>
          <span
            v-if="sec.key === 'system'"
            class="badge"
            style="color: var(--accent-emerald)"
          >
            ready
          </span>
          <span
            v-else
            class="badge"
            :style="{
              color:
                statusByKind.get(sec.key)?.state === 'ready'
                  ? 'var(--accent-emerald)'
                  : statusByKind.get(sec.key)?.state === 'degraded'
                    ? 'var(--accent-rose)'
                    : 'var(--text-secondary)',
            }"
          >
            {{ statusByKind.get(sec.key)?.state || 'disabled' }}
          </span>
        </div>
        <span class="muted" style="font-size: 11px">{{ sec.phase }}</span>
      </button>
    </aside>

    <!-- Right Independent Config Panel -->
    <section class="stack panel-col">
      <div v-if="errMsg" class="error-banner">{{ errMsg }}</div>
      <div v-if="msg" class="card" style="border-color: var(--accent-emerald)">{{ msg }}</div>

      <!-- 1. Base System Panel -->
      <div v-if="activeSection === 'system'" class="card stack">
        <div class="row" style="justify-content: space-between">
          <div>
            <h2 style="margin: 0; font-size: 16px">{{ currentMeta.title }}</h2>
            <span class="muted">{{ currentMeta.description }}</span>
          </div>
          <button class="primary" :disabled="saving" @click="saveSystem">
            {{ saving ? '保存中…' : '保存基础配置' }}
          </button>
        </div>

        <div class="grid-2">
          <label class="field">
            <span>部署模式</span>
            <select v-model="draft.server.mode">
              <option value="standalone">standalone (单机 · 自动监听 127.0.0.1)</option>
              <option value="server">server (常驻节点 · 自动监听 0.0.0.0)</option>
            </select>
          </label>
          <label class="field">
            <span>监听端口</span>
            <input v-model.number="draft.server.port" type="number" />
          </label>
        </div>

        <label class="field">
          <span>数据目录（SQLite 台账与附件目录）</span>
          <input v-model="draft.storage.data_dir" />
        </label>

        <div class="sub-card stack">
          <div class="row" style="justify-content: space-between; flex-wrap: wrap">
            <div>
              <strong>自定义任务状态流（workflow.task_statuses）</strong>
              <div class="muted" style="font-size: 12px">
                可按个人工作流增删改状态名称、分类与颜色，保存后立即同步到任务台账与星轨总览。
              </div>
            </div>
            <div class="row">
              <button type="button" @click="addStatusRow">+ 新增状态</button>
              <button type="button" class="ghost" @click="resetDefaultStatuses">恢复默认</button>
            </div>
          </div>

          <div
            v-for="(st, idx) in draft.workflow.task_statuses"
            :key="idx"
            class="status-row"
          >
            <input v-model="st.key" placeholder="状态键 (如 in_progress)" />
            <input v-model="st.label" placeholder="显示名称" />
            <select v-model="st.category">
              <option value="todo">todo (待办)</option>
              <option value="active">active (进行中)</option>
              <option value="blocked">blocked (阻塞)</option>
              <option value="done">done (结束)</option>
            </select>
            <input v-model="st.color" type="color" class="color-input" title="状态颜色" />
            <div class="row" style="gap: 4px">
              <button type="button" class="ghost" :disabled="idx === 0" @click="moveStatusRow(idx, -1)">↑</button>
              <button
                type="button"
                class="ghost"
                :disabled="idx === draft.workflow.task_statuses.length - 1"
                @click="moveStatusRow(idx, 1)"
              >↓</button>
              <button
                type="button"
                class="ghost danger"
                :disabled="draft.workflow.task_statuses.length <= 1"
                @click="removeStatusRow(idx)"
              >删</button>
            </div>
          </div>
        </div>

        <div class="muted" style="font-size: 12px">
          监听地址随部署模式自动推导（<code>standalone → 127.0.0.1</code>，<code>server → 0.0.0.0</code>），高级场景可在启动命令使用 <code>--host</code> 覆盖；修改模式或端口后需重启 <code>lagrangian-server</code> 生效。
        </div>
      </div>

      <!-- 2. Git & CodeHost Component Panel (Phase 1 Local + Phase 2 GitLab) -->
      <div v-else-if="activeSection === 'code_host'" class="card stack">
        <div class="row" style="justify-content: space-between">
          <div>
            <div class="row">
              <h2 style="margin: 0; font-size: 16px">{{ currentMeta.title }}</h2>
              <span class="badge"><code>{{ currentMeta.contract }}</code></span>
            </div>
            <span class="muted">{{ currentMeta.description }}</span>
          </div>
          <button class="primary" :disabled="saving" @click="saveCodeHostComponent">
            {{ saving ? '保存中…' : '保存本地 Git 规则' }}
          </button>
        </div>

        <div class="sub-card stack">
          <strong>本地 Git 提交与归并规则</strong>
          <label class="field">
            <span>Git 提交作者别名（逗号分隔，用于扫描本地与远端分支时筛选个人提交）</span>
            <input v-model="aliasesText" placeholder="例如：alice, alice@example.com" />
          </label>

          <label class="row" style="cursor: pointer">
            <input v-model="draft.git.auto_reconcile_rebase" type="checkbox" style="width: auto" />
            <span>启用基于 git patch-id 一对一核验的 Rebase 自动归并</span>
          </label>
        </div>

        <div class="sub-card stack">
          <div class="row" style="justify-content: space-between">
            <strong>远端代码托管连接实例 ({{ gitlabConnections.length }})</strong>
            <span class="badge">GitLab v4 / GitHub v3 · 凭据隔离存储</span>
          </div>

          <div v-if="gitlabConnections.length === 0" class="muted" style="font-size: 12px">
            尚未配置 GitLab 或 GitHub 实例。配置后可在「集成中心」浏览 MR / PR 并在任务台账拉取远端分支与已合并提交。
          </div>

          <div v-for="c in gitlabConnections" :key="c.id" class="conn-item stack">
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <div class="row">
                <strong>{{ c.name }}</strong>
                <span class="badge">{{ c.driver }}</span>
                <span class="badge">{{ c.auth_type }}</span>
                <span v-if="c.has_secret" class="badge" style="color: var(--accent-emerald)">已存凭据</span>
                <span
                  class="badge"
                  :style="{
                    color: !c.enabled
                      ? 'var(--text-secondary)'
                      : c.last_error
                        ? 'var(--accent-rose)'
                        : 'var(--accent-emerald)',
                  }"
                >
                  {{ !c.enabled ? '已停用' : c.last_error ? '异常' : '就绪' }}
                </span>
              </div>
              <div class="row">
                <button class="ghost" :disabled="busyId === c.id" @click="startEditConn(c)">
                  {{ editingConnId === c.id ? '收起编辑' : '编辑' }}
                </button>
                <button :disabled="busyId === c.id" @click="testConn(c)">测试连通性</button>
                <button class="ghost" :disabled="busyId === c.id" @click="toggleConn(c)">
                  {{ c.enabled ? '停用' : '启用' }}
                </button>
                <button class="ghost danger" :disabled="busyId === c.id" @click="removeConn(c)">删除</button>
              </div>
            </div>
            <div class="muted" style="font-size: 12px">
              地址：<code>{{ c.base_url }}</code>
              <span v-if="c.last_checked_at"> · 最近检测：{{ c.last_checked_at }}</span>
            </div>
            <div v-if="c.last_error" style="font-size: 12px; color: var(--accent-rose)">
              错误：{{ c.last_error }}
            </div>

            <!-- Inline CodeHost Edit Form -->
            <div v-if="editingConnId === c.id" class="form-box stack">
              <strong style="font-size: 13px">编辑 {{ c.driver === 'github' ? 'GitHub' : 'GitLab' }} 连接</strong>
              <div class="grid-even">
                <label class="field">
                  <span>连接名称</span>
                  <input v-model="editForm.name" />
                </label>
                <label class="field">
                  <span>服务地址（Base URL）</span>
                  <input v-model="editForm.base_url" />
                </label>
              </div>
              <div v-if="editForm.driver === 'gitlab'" class="grid-3-even">
                <label class="field">
                  <span>认证方式</span>
                  <select v-model="editForm.auth_type">
                    <option value="pat">Personal Access Token (PAT，推荐)</option>
                    <option value="oauth_password">OAuth2 密码登录 (/oauth/token)</option>
                  </select>
                </label>
                <label class="field">
                  <span>{{ editForm.auth_type === 'oauth_password' ? '用户名（必填）' : '用户名（可选）' }}</span>
                  <input v-model="editForm.username" />
                </label>
                <label class="field">
                  <span>新 Token / 密码（留空保持原凭据不变）</span>
                  <input v-model="editForm.secret" type="password" placeholder="不修改请留空" />
                </label>
              </div>
              <label v-else class="field">
                <span>新 GitHub Personal Access Token（留空保持原凭据不变）</span>
                <input v-model="editForm.secret" type="password" placeholder="不修改请留空" />
              </label>
              <div class="row" style="justify-content: space-between; flex-wrap: wrap">
                <span
                  class="muted"
                  style="font-size: 12px"
                  :style="{ color: isEditReadyToSave ? 'var(--accent-emerald)' : undefined }"
                >
                  {{
                    editNeedsTest
                      ? isEditReadyToSave
                        ? `✓ 连通性已通过（${editVerified.identity}）`
                        : '凭据或地址已变动（或原连接异常），请先测试连通性再保存'
                      : '仅修改名称可直接保存'
                  }}
                </span>
                <div class="row">
                  <button class="ghost" @click="cancelEditConn">取消</button>
                  <button :disabled="testingDraft || saving" @click="testEditConn">
                    {{ testingDraft ? '测试中…' : '测试连通性' }}
                  </button>
                  <button
                    class="primary"
                    :disabled="saving || testingDraft || !isEditReadyToSave"
                    @click="saveEditConn"
                  >
                    保存修改
                  </button>
                </div>
              </div>
            </div>
          </div>

          <div class="form-box stack">
            <strong style="font-size: 13px">+ 新增代码托管实例（GitLab / GitHub）</strong>
            <div class="grid-3-even">
              <label class="field">
                <span>平台类型</span>
                <select v-model="gitlabForm.driver" @change="onCodeHostDriverChange">
                  <option value="gitlab">GitLab (REST API v4)</option>
                  <option value="github">GitHub (REST API v3)</option>
                </select>
              </label>
              <label class="field">
                <span>连接名称</span>
                <input
                  v-model="gitlabForm.name"
                  :placeholder="gitlabForm.driver === 'github' ? '例如：个人 GitHub' : '例如：主代码仓 GitLab'"
                />
              </label>
              <label class="field">
                <span>{{ gitlabForm.driver === 'github' ? 'API 地址（公有云填 https://api.github.com）' : '服务地址（Base URL）' }}</span>
                <input
                  v-model="gitlabForm.base_url"
                  :placeholder="gitlabForm.driver === 'github' ? 'https://api.github.com' : 'https://gitlab.example.com'"
                />
              </label>
            </div>
            <div v-if="gitlabForm.driver === 'gitlab'" class="grid-3-even">
              <label class="field">
                <span>认证方式</span>
                <select v-model="gitlabForm.auth_type">
                  <option value="pat">Personal Access Token (PAT，推荐)</option>
                  <option value="oauth_password">OAuth2 密码登录 (/oauth/token)</option>
                </select>
              </label>
              <label class="field">
                <span>{{ gitlabForm.auth_type === 'oauth_password' ? '用户名（必填）' : '用户名（PAT 可留空）' }}</span>
                <input
                  v-model="gitlabForm.username"
                  :placeholder="gitlabForm.auth_type === 'oauth_password' ? 'GitLab 登录用户名' : '可选'"
                />
              </label>
              <label class="field">
                <span>{{
                  gitlabForm.auth_type === 'oauth_password'
                    ? '登录密码（仅换取 OAuth Token，不存明文密码）'
                    : 'Personal Access Token（需 read_api 权限）'
                }}</span>
                <input
                  v-model="gitlabForm.secret"
                  type="password"
                  :placeholder="gitlabForm.auth_type === 'oauth_password' ? '输入 GitLab 登录密码' : 'glpat-...'"
                />
              </label>
            </div>
            <label v-else class="field">
              <span>GitHub Personal Access Token（需 repo 权限，默认 PAT 认证）</span>
              <input v-model="gitlabForm.secret" type="password" placeholder="ghp_... 或 github_pat_..." />
            </label>
            <div v-if="gitlabForm.driver === 'gitlab' && gitlabForm.auth_type === 'oauth_password'" class="muted" style="font-size: 12px">
              GitLab REST API v4 不支持账号密码 Basic Auth；此模式通过 <code>POST /oauth/token</code> 换取临时会话令牌。若实例禁用密码授权或账号已开启 2FA，请改用 PAT。
            </div>
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <span
                class="muted"
                style="font-size: 12px"
                :style="{ color: isGitLabDraftReady ? 'var(--accent-emerald)' : undefined }"
              >
                {{
                  isGitLabDraftReady
                    ? `✓ 连通性已通过（${gitlabDraftVerified.identity}）`
                    : '请先测试连通性，通过后才允许保存连接'
                }}
              </span>
              <div class="row">
                <button :disabled="testingDraft || saving" @click="testGitLabDraft">
                  {{ testingDraft ? '测试中…' : '测试连通性' }}
                </button>
                <button
                  class="primary"
                  :disabled="saving || testingDraft || !isGitLabDraftReady"
                  @click="addGitLabConnection"
                >
                  添加 {{ gitlabForm.driver === 'github' ? 'GitHub' : 'GitLab' }} 连接
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 3. IssueTracker (Jira) Component Panel (Phase 2) -->
      <div v-else-if="activeSection === 'issue_tracker'" class="card stack">
        <div class="row" style="justify-content: space-between">
          <div>
            <div class="row">
              <h2 style="margin: 0; font-size: 16px">{{ currentMeta.title }}</h2>
              <span class="badge"><code>{{ currentMeta.contract }}</code></span>
              <span class="badge">REST API v2</span>
            </div>
            <span class="muted">{{ currentMeta.description }}</span>
          </div>
        </div>

        <div class="sub-card stack">
          <div class="row" style="justify-content: space-between">
            <strong>Jira 连接实例 ({{ jiraConnections.length }})</strong>
            <span class="badge">支持任意项目与自定义状态白名单</span>
          </div>

          <div v-if="jiraConnections.length === 0" class="muted" style="font-size: 12px">
            尚未配置 Jira 实例。添加后可在「集成中心」按自定义状态白名单检索工单、标记本地搁置/复测状态并关联任务。
          </div>

          <div v-for="c in jiraConnections" :key="c.id" class="conn-item stack">
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <div class="row">
                <strong>{{ c.name }}</strong>
                <span class="badge">{{ c.auth_type }}</span>
                <span v-if="c.has_secret" class="badge" style="color: var(--accent-emerald)">已存凭据</span>
                <span
                  class="badge"
                  :style="{
                    color: !c.enabled
                      ? 'var(--text-secondary)'
                      : c.last_error
                        ? 'var(--accent-rose)'
                        : 'var(--accent-emerald)',
                  }"
                >
                  {{ !c.enabled ? '已停用' : c.last_error ? '异常' : '就绪' }}
                </span>
              </div>
              <div class="row">
                <button class="ghost" :disabled="busyId === c.id" @click="startEditConn(c)">
                  {{ editingConnId === c.id ? '收起编辑' : '编辑' }}
                </button>
                <button :disabled="busyId === c.id" @click="testConn(c)">测试连通性</button>
                <button class="ghost" :disabled="busyId === c.id" @click="toggleConn(c)">
                  {{ c.enabled ? '停用' : '启用' }}
                </button>
                <button class="ghost danger" :disabled="busyId === c.id" @click="removeConn(c)">删除</button>
              </div>
            </div>
            <div class="muted" style="font-size: 12px">
              地址：<code>{{ c.base_url }}</code>
              <span v-if="c.options?.default_project"> · 默认项目：<code>{{ c.options.default_project }}</code></span>
              <span v-if="c.options?.inspection_statuses"> · 巡检状态白名单：{{ c.options.inspection_statuses }}</span>
            </div>
            <div v-if="c.last_error" style="font-size: 12px; color: var(--accent-rose)">
              错误：{{ c.last_error }}
            </div>

            <!-- Inline Jira Edit Form -->
            <div v-if="editingConnId === c.id" class="form-box stack">
              <strong style="font-size: 13px">编辑 Jira 连接</strong>
              <div class="grid-even">
                <label class="field">
                  <span>连接名称</span>
                  <input v-model="editForm.name" />
                </label>
                <label class="field">
                  <span>服务地址（Base URL）</span>
                  <input v-model="editForm.base_url" />
                </label>
              </div>
              <div class="grid-3-even">
                <label class="field">
                  <span>认证方式</span>
                  <select v-model="editForm.auth_type">
                    <option value="pat">Bearer Token (PAT)</option>
                    <option value="basic">Basic Auth (账号/API Token)</option>
                  </select>
                </label>
                <label class="field">
                  <span>用户名（Basic 必填：Server 填短用户名，Cloud 填邮箱）</span>
                  <input v-model="editForm.username" placeholder="Jira Server 填短用户名，Jira Cloud 填邮箱" />
                </label>
                <label class="field">
                  <span>新 Token / 密码（留空保持原凭据不变）</span>
                  <input v-model="editForm.secret" type="password" placeholder="不修改请留空" />
                </label>
              </div>
              <div class="grid-even">
                <label class="field">
                  <span>默认项目键（可选）</span>
                  <input v-model="editForm.default_project" placeholder="留空表示跨项目查询" />
                </label>
                <label class="field">
                  <span>默认巡检状态白名单（留空不限状态，逗号分隔）</span>
                  <input
                    v-model="editForm.inspection_statuses"
                    placeholder="留空不限状态；例如：开放, 处理中, 重新打开"
                  />
                </label>
              </div>
              <div v-if="loadingEditStatuses" class="muted" style="font-size: 12px">
                正在读取当前 Jira 实例的可选状态列表…
              </div>
              <div v-else-if="editVerified.statuses.length" class="stack" style="gap: 6px">
                <span class="muted" style="font-size: 12px">
                  已从当前 Jira 实例读取到以下状态（点击快速加入/移出白名单）：
                </span>
                <div class="row" style="flex-wrap: wrap; gap: 6px">
                  <button
                    v-for="st in editVerified.statuses"
                    :key="st"
                    type="button"
                    :class="{ primary: editSelectedJiraStatuses.includes(st) }"
                    style="padding: 3px 9px; font-size: 12px"
                    @click="toggleEditJiraStatusChip(st)"
                  >
                    {{ editSelectedJiraStatuses.includes(st) ? '✓ ' : '+ ' }}{{ st }}
                  </button>
                </div>
              </div>
              <div class="row" style="justify-content: space-between; flex-wrap: wrap">
                <span
                  class="muted"
                  style="font-size: 12px"
                  :style="{ color: isEditReadyToSave ? 'var(--accent-emerald)' : undefined }"
                >
                  {{
                    editNeedsTest
                      ? isEditReadyToSave
                        ? `✓ 连通性已通过（${editVerified.identity}）`
                        : '凭据或地址已变动（或原连接异常），请先测试连通性再保存'
                      : '仅修改名称、项目或白名单可直接保存（也可点测试以拉取状态列表）'
                  }}
                </span>
                <div class="row">
                  <button class="ghost" @click="cancelEditConn">取消</button>
                  <button :disabled="testingDraft || saving" @click="testEditConn">
                    {{ testingDraft ? '测试中…' : '测试连通性' }}
                  </button>
                  <button
                    class="primary"
                    :disabled="saving || testingDraft || !isEditReadyToSave"
                    @click="saveEditConn"
                  >
                    保存修改
                  </button>
                </div>
              </div>
            </div>
          </div>

          <div class="form-box stack">
            <strong style="font-size: 13px">+ 新增 Jira 实例</strong>
            <div class="grid-even">
              <label class="field">
                <span>连接名称</span>
                <input v-model="jiraForm.name" placeholder="例如：研发 Jira" />
              </label>
              <label class="field">
                <span>服务地址（Base URL）</span>
                <input v-model="jiraForm.base_url" placeholder="https://jira.example.com" />
              </label>
            </div>
            <div class="grid-3-even">
              <label class="field">
                <span>认证方式</span>
                <select v-model="jiraForm.auth_type">
                  <option value="pat">Bearer Token (PAT)</option>
                  <option value="basic">Basic Auth (账号/API Token)</option>
                </select>
              </label>
              <label class="field">
                <span>用户名（Basic 必填：Server 填短用户名，Cloud 填邮箱）</span>
                <input v-model="jiraForm.username" placeholder="Jira Server 填短用户名，Jira Cloud 填邮箱" />
              </label>
              <label class="field">
                <span>Token / 密码（仅入安全存储）</span>
                <input v-model="jiraForm.secret" type="password" placeholder="输入 Token 或密码" />
              </label>
            </div>
            <div class="grid-even">
              <label class="field">
                <span>默认项目键（可选，例如 PROJ）</span>
                <input v-model="jiraForm.default_project" placeholder="留空表示跨项目查询" />
              </label>
              <label class="field">
                <span>默认巡检状态白名单（留空不限状态，多个用逗号分隔）</span>
                <input
                  v-model="jiraForm.inspection_statuses"
                  placeholder="留空不限状态；例如：开放, 处理中, 重新打开"
                />
              </label>
            </div>
            <div v-if="isJiraDraftReady && jiraDraftVerified.statuses.length" class="stack" style="gap: 6px">
              <span class="muted" style="font-size: 12px">
                已从当前 Jira 实例读取到以下状态（点击快速加入/移出白名单）：
              </span>
              <div class="row" style="flex-wrap: wrap; gap: 6px">
                <button
                  v-for="st in jiraDraftVerified.statuses"
                  :key="st"
                  type="button"
                  :class="{ primary: selectedJiraStatuses.includes(st) }"
                  style="padding: 3px 9px; font-size: 12px"
                  @click="toggleJiraStatusChip(st)"
                >
                  {{ selectedJiraStatuses.includes(st) ? '✓ ' : '+ ' }}{{ st }}
                </button>
              </div>
            </div>
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <span
                class="muted"
                style="font-size: 12px"
                :style="{ color: isJiraDraftReady ? 'var(--accent-emerald)' : undefined }"
              >
                {{
                  isJiraDraftReady
                    ? `✓ 连通性已通过（${jiraDraftVerified.identity}）`
                    : '请先测试连通性，通过后才允许保存连接'
                }}
              </span>
              <div class="row">
                <button :disabled="testingDraft || saving" @click="testJiraDraft">
                  {{ testingDraft ? '测试中…' : '测试连通性' }}
                </button>
                <button
                  class="primary"
                  :disabled="saving || testingDraft || !isJiraDraftReady"
                  @click="addJiraConnection"
                >
                  添加 Jira 连接
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 4. KnowledgeBase Component Panel (Phase 2) -->
      <div v-else-if="activeSection === 'knowledge_base'" class="card stack">
        <div class="row" style="justify-content: space-between">
          <div>
            <div class="row">
              <h2 style="margin: 0; font-size: 16px">{{ currentMeta.title }}</h2>
              <span class="badge"><code>{{ currentMeta.contract }}</code></span>
              <span class="badge">SQLite FTS5</span>
            </div>
            <span class="muted">{{ currentMeta.description }}</span>
          </div>
        </div>

        <div class="sub-card stack">
          <div class="row" style="justify-content: space-between">
            <strong>已配置知识库 ({{ knowledgeBases.length }})</strong>
            <span class="badge">支持本地目录与远端 Git 双向同步</span>
          </div>

          <div v-if="knowledgeBases.length === 0" class="muted" style="font-size: 12px">
            尚未配置知识库。添加本地 Markdown 目录或远端 Git 仓库后，系统将自动建立 FTS5 全文索引并在「知识库」视图中可用。
          </div>

          <div v-for="kb in knowledgeBases" :key="kb.id" class="conn-item stack">
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <div class="row">
                <strong>{{ kb.name }}</strong>
                <span class="badge">{{ kb.source_type === 'local_dir' ? '本地目录' : '远端 Git' }}</span>
                <span class="badge">{{ kb.readonly ? '只读' : '可编辑' }}</span>
                <span class="badge">{{ kb.doc_count }} 篇文档</span>
                <span v-if="kb.last_commit_sha" class="badge"><code>{{ kb.last_commit_sha.slice(0, 8) }}</code></span>
              </div>
              <div class="row">
                <button :disabled="busyId === kb.id" @click="syncKB(kb)">
                  {{ busyId === kb.id ? '同步中…' : '立即同步' }}
                </button>
                <button class="ghost danger" :disabled="busyId === kb.id" @click="removeKB(kb)">移除</button>
              </div>
            </div>
            <div class="muted" style="font-size: 12px">
              源位置：<code>{{ kb.location }}</code>
              <span v-if="kb.branch"> · 分支：<code>{{ kb.branch }}</code></span>
              <span v-if="kb.last_synced_at"> · 最近同步：{{ kb.last_synced_at }}</span>
            </div>
            <div v-if="kb.last_error" style="font-size: 12px; color: var(--accent-rose)">
              同步错误：{{ kb.last_error }}
            </div>
          </div>

          <div class="form-box stack">
            <strong style="font-size: 13px">+ 新增知识库</strong>
            <div class="grid-even">
              <label class="field">
                <span>知识库名称</span>
                <input v-model="kbForm.name" placeholder="例如：工程规范手册 / 个人架构笔记" />
              </label>
              <label class="field">
                <span>来源类型</span>
                <select v-model="kbForm.source_type">
                  <option value="local_dir">local_dir（本地 Markdown 目录）</option>
                  <option value="remote_git">remote_git（远端 Git 仓库，支持可写自动推送或只读）</option>
                </select>
              </label>
            </div>
            <label class="field">
              <span>{{ kbForm.source_type === 'local_dir' ? '本地目录绝对路径' : 'Git 仓库地址（HTTP/HTTPS/SSH/本地路径）' }}</span>
              <input
                v-model="kbForm.location"
                :placeholder="kbForm.source_type === 'local_dir' ? '/path/to/docs' : 'https://git.example.com/team/wiki.git'"
              />
            </label>
            <div v-if="kbForm.source_type === 'remote_git'" class="grid-even">
              <label class="field">
                <span>跟踪分支（可选，留空用默认分支）</span>
                <input v-model="kbForm.branch" placeholder="main" />
              </label>
              <label class="field">
                <span>访问凭据 Token（私有 HTTPS 仓库必填，可写模式需具备 write_repository 权限）</span>
                <input v-model="kbForm.secret" type="password" placeholder="公开或 SSH 仓库可留空" />
              </label>
            </div>
            <label class="row" style="cursor: pointer">
              <input v-model="kbForm.readonly" type="checkbox" style="width: auto" />
              <span>{{
                kbForm.source_type === 'remote_git'
                  ? '以只读模式挂载（取消勾选则保存文档时自动安全变基并 commit + push 回远端分支）'
                  : '以只读模式挂载（禁止在页面中编辑写入）'
              }}</span>
            </label>
            <div class="row" style="justify-content: flex-end">
              <button class="primary" :disabled="saving" @click="addKnowledgeBase">
                添加并建立索引
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- 5. Phase 3-4 Component Slot Info Panels -->
      <div v-else class="card stack">
        <div class="row" style="justify-content: space-between">
          <div>
            <div class="row">
              <h2 style="margin: 0; font-size: 16px">{{ currentMeta.title }}</h2>
              <span class="badge"><code>{{ currentMeta.contract }}</code></span>
              <span class="badge">{{ currentMeta.phase }}</span>
            </div>
            <span class="muted">{{ currentMeta.description }}</span>
          </div>
        </div>

        <div class="sub-card stack">
          <div class="row" style="justify-content: space-between">
            <strong>当前运行时状态</strong>
            <span class="badge">{{ statusByKind.get(activeSection)?.state || 'disabled' }}</span>
          </div>
          <p class="muted" style="margin: 0">
            {{ statusByKind.get(activeSection)?.detail || '尚未配置组件实例' }}
          </p>
          <p class="muted" style="margin: 0; font-size: 12px">
            该组件的接口契约与配置槽位已预留，具体适配器表单与连通性测试将在
            <strong>{{ currentMeta.phase }}</strong> 落地。
          </p>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.settings-layout {
  display: grid;
  grid-template-columns: 260px 1fr;
  gap: 14px;
  align-items: start;
}
.nav-item {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 4px;
  width: 100%;
  text-align: left;
  background: rgba(7, 11, 20, 0.4);
  border: 1px solid var(--border-subtle);
  padding: 9px 10px;
}
.nav-item.active {
  border-color: var(--accent-cyan);
  background: rgba(56, 189, 248, 0.12);
}
.sub-card {
  background: rgba(7, 11, 20, 0.45);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 12px 14px;
}
.conn-item {
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 10px 12px;
  background: rgba(15, 23, 42, 0.55);
}
.form-box {
  border-top: 1px dashed var(--border-strong);
  padding-top: 12px;
  margin-top: 4px;
}
.grid-2 {
  display: grid;
  grid-template-columns: 1fr 160px;
  gap: 10px;
}
.grid-even {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.grid-3-even {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  gap: 10px;
}
.status-row {
  display: grid;
  grid-template-columns: 130px 1fr 140px 44px auto;
  gap: 8px;
  align-items: center;
}
.color-input {
  padding: 2px;
  height: 32px;
  cursor: pointer;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.field span {
  font-size: 12px;
  color: var(--text-secondary);
}
@media (max-width: 900px) {
  .settings-layout,
  .grid-2,
  .grid-even,
  .grid-3-even,
  .status-row {
    grid-template-columns: 1fr;
  }
}
</style>
