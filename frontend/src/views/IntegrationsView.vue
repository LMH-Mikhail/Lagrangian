<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api } from '../api/client'
import type {
  GitLabMR,
  GitLabProject,
  IntegrationConnection,
  JiraIssueWithFollowup,
  Task,
} from '../types/models'

const emit = defineEmits<{
  (e: 'changed'): void
  (e: 'open-settings'): void
}>()

type SubTab = 'jira' | 'gitlab'

const subTab = ref<SubTab>('jira')
const connections = ref<IntegrationConnection[]>([])
const tasks = ref<Task[]>([])
const loading = ref(false)
const errMsg = ref('')
const noticeMsg = ref('')

// Jira state
const selectedJiraId = ref('')
const jiraFilter = reactive({
  project: '',
  statuses: '',
  q: '',
  onlyMe: true,
})
const jiraIssues = ref<JiraIssueWithFollowup[]>([])
const localStateFilter = ref('')
const followupDrafts = reactive<Record<string, { state: string; reason: string }>>({})

// GitLab state
const selectedGitLabId = ref('')
const gitlabFilter = reactive({
  project: '',
  scope: 'all',
  state: 'opened',
  q: '',
})
const gitlabProjects = ref<GitLabProject[]>([])
const gitlabMRs = ref<GitLabMR[]>([])

// Task linking selection per remote key
const linkTaskMap = reactive<Record<string, string>>({})

const jiraConns = computed(() =>
  connections.value.filter((c) => c.kind === 'issue_tracker' && c.enabled),
)
const gitlabConns = computed(() =>
  connections.value.filter((c) => c.kind === 'code_host' && c.enabled),
)

const activeJiraConn = computed(() =>
  jiraConns.value.find((c) => c.id === selectedJiraId.value) || null,
)

const filteredJiraIssues = computed(() => {
  if (!localStateFilter.value) return jiraIssues.value
  return jiraIssues.value.filter((item) => item.local_state === localStateFilter.value)
})

const localStateMeta: Record<string, { label: string; color: string }> = {
  active: { label: '跟踪中', color: 'var(--accent-cyan)' },
  paused: { label: '已搁置', color: '#f59e0b' },
  pending_retest: { label: '待复测', color: '#c084fc' },
  verified: { label: '已验收', color: 'var(--accent-emerald)' },
}

onMounted(loadInit)

async function loadInit() {
  errMsg.value = ''
  try {
    const [connRes, taskRes] = await Promise.all([
      api.listConnections(),
      api.listTasks({ include_archived: false }),
    ])
    connections.value = connRes.connections
    tasks.value = taskRes.tasks
    if (!selectedJiraId.value && jiraConns.value.length > 0) {
      selectedJiraId.value = jiraConns.value[0].id
    }
    if (!selectedGitLabId.value && gitlabConns.value.length > 0) {
      selectedGitLabId.value = gitlabConns.value[0].id
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载集成中心初始化数据失败'
  }
}

async function queryJira() {
  if (!selectedJiraId.value) return
  loading.value = true
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    const res = await api.jiraIssues(selectedJiraId.value, {
      project: jiraFilter.project.trim(),
      statuses: jiraFilter.statuses.trim(),
      assignee: jiraFilter.onlyMe ? 'me' : '',
      q: jiraFilter.q.trim(),
    })
    jiraIssues.value = res.issues
    for (const item of res.issues) {
      followupDrafts[item.key] = {
        state: item.local_state || 'active',
        reason: item.reason || '',
      }
      if (!linkTaskMap[item.key] && tasks.value.length > 0) {
        linkTaskMap[item.key] = tasks.value[0].id
      }
    }
    noticeMsg.value = `查询完成，返回 ${res.issues.length} 条 Jira 工单。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '查询 Jira 工单失败'
  } finally {
    loading.value = false
  }
}

async function saveFollowup(issue: JiraIssueWithFollowup) {
  if (!selectedJiraId.value) return
  const draft = followupDrafts[issue.key] || { state: issue.local_state, reason: issue.reason }
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    const saved = await api.setFollowup({
      connection_id: selectedJiraId.value,
      issue_key: issue.key,
      title: issue.title,
      web_url: issue.web_url,
      remote_state: issue.status,
      local_state: draft.state,
      reason: draft.reason,
    })
    issue.local_state = saved.local_state
    issue.reason = saved.reason
    noticeMsg.value = `已更新 ${issue.key} 本地跟踪状态为「${localStateMeta[saved.local_state]?.label || saved.local_state}」（不回写远端 Jira）。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '保存本地跟踪状态失败'
  }
}

async function linkJiraIssue(issue: JiraIssueWithFollowup) {
  const taskId = linkTaskMap[issue.key]
  if (!selectedJiraId.value || !taskId) {
    errMsg.value = '请先选择要关联的目标任务。'
    return
  }
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    await api.linkRemote(taskId, {
      connection_id: selectedJiraId.value,
      remote_kind: 'issue',
      remote_key: issue.key,
      title: issue.title,
      remote_state: issue.status,
      web_url: issue.web_url,
    })
    emit('changed')
    const target = tasks.value.find((t) => t.id === taskId)
    noticeMsg.value = `已将工单 ${issue.key} 关联到任务「${target?.title || taskId}」。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '关联工单失败'
  }
}

async function loadGitLabProjects() {
  if (!selectedGitLabId.value) return
  errMsg.value = ''
  try {
    const res = await api.gitlabProjects(selectedGitLabId.value, gitlabFilter.project.trim())
    gitlabProjects.value = res.projects
    noticeMsg.value = `已加载 ${res.projects.length} 个可见 GitLab 项目。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载 GitLab 项目列表失败'
  }
}

async function queryGitLabMRs() {
  if (!selectedGitLabId.value) return
  loading.value = true
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    const res = await api.gitlabMRs(selectedGitLabId.value, {
      project: gitlabFilter.project.trim(),
      scope: gitlabFilter.scope,
      state: gitlabFilter.state,
      q: gitlabFilter.q.trim(),
    })
    gitlabMRs.value = res.merge_requests
    for (const mr of res.merge_requests) {
      const k = `mr:${mr.id}`
      if (!linkTaskMap[k] && tasks.value.length > 0) {
        linkTaskMap[k] = tasks.value[0].id
      }
    }
    noticeMsg.value = `查询完成，返回 ${res.merge_requests.length} 条 Merge Request。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '查询 GitLab MR 失败'
  } finally {
    loading.value = false
  }
}

async function linkMR(mr: GitLabMR) {
  const k = `mr:${mr.id}`
  const taskId = linkTaskMap[k]
  if (!selectedGitLabId.value || !taskId) {
    errMsg.value = '请先选择要关联的目标任务。'
    return
  }
  errMsg.value = ''
  noticeMsg.value = ''
  const remoteKey = mr.id.includes('#') || mr.id.includes('!') ? mr.id : `!${mr.id}`
  try {
    await api.linkRemote(taskId, {
      connection_id: selectedGitLabId.value,
      remote_kind: 'merge_request',
      remote_key: remoteKey,
      title: mr.title,
      remote_state: mr.state,
      web_url: mr.web_url,
    })
    emit('changed')
    const target = tasks.value.find((t) => t.id === taskId)
    noticeMsg.value = `已将 ${remoteKey} 关联到任务「${target?.title || taskId}」。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '关联 MR / PR 失败'
  }
}
</script>

<template>
  <div class="stack">
    <div class="card row" style="justify-content: space-between; flex-wrap: wrap">
      <div>
        <h2 style="margin: 0; font-size: 16px">外部平台集成中心</h2>
        <span class="muted">
          基于可插拔 CodeHost (GitLab) 与 IssueTracker (Jira) 连接实时检索工单与 MR，支持本地搁置跟踪与任务关联。
        </span>
      </div>
      <div class="row">
        <button :class="{ primary: subTab === 'jira' }" @click="subTab = 'jira'">
          Jira 工单 ({{ jiraConns.length }})
        </button>
        <button :class="{ primary: subTab === 'gitlab' }" @click="subTab = 'gitlab'">
          代码托管 MR / PR ({{ gitlabConns.length }})
        </button>
        <button class="ghost" @click="emit('open-settings')">管理连接</button>
      </div>
    </div>

    <div v-if="errMsg" class="error-banner">{{ errMsg }}</div>
    <div v-if="noticeMsg" class="card" style="border-color: var(--accent-emerald)">{{ noticeMsg }}</div>

    <!-- Jira Issues Sub-tab -->
    <section v-if="subTab === 'jira'" class="card stack">
      <div v-if="jiraConns.length === 0" class="stack" style="align-items: center; padding: 32px 12px">
        <p class="muted" style="margin: 0">尚未配置已启用的 Jira 连接实例。</p>
        <button class="primary" @click="emit('open-settings')">前往「配置与组件 → 工单系统」添加 Jira 连接</button>
      </div>

      <template v-else>
        <div class="filter-grid">
          <label class="field">
            <span>Jira 实例</span>
            <select v-model="selectedJiraId">
              <option v-for="c in jiraConns" :key="c.id" :value="c.id">
                {{ c.name }} ({{ c.base_url }})
              </option>
            </select>
          </label>
          <label class="field">
            <span>项目键（留空用连接默认）</span>
            <input
              v-model="jiraFilter.project"
              :placeholder="activeJiraConn?.options?.default_project || '如 PROJ'"
            />
          </label>
          <label class="field">
            <span>状态白名单覆盖（逗号分隔）</span>
            <input
              v-model="jiraFilter.statuses"
              :placeholder="activeJiraConn?.options?.inspection_statuses || '留空沿用连接白名单，多个用逗号分隔'"
            />
          </label>
          <label class="field">
            <span>关键词 / 工单号</span>
            <input v-model="jiraFilter.q" placeholder="搜索摘要或工单号…" @keyup.enter="queryJira" />
          </label>
        </div>

        <div class="row" style="justify-content: space-between; flex-wrap: wrap">
          <div class="row" style="flex-wrap: wrap">
            <label class="row" style="cursor: pointer">
              <input v-model="jiraFilter.onlyMe" type="checkbox" style="width: auto" />
              <span>仅看指派给我 (currentUser)</span>
            </label>
            <span class="muted">| 本地状态筛选：</span>
            <button
              v-for="opt in [
                { key: '', label: '全部' },
                { key: 'active', label: '跟踪中' },
                { key: 'paused', label: '已搁置' },
                { key: 'pending_retest', label: '待复测' },
                { key: 'verified', label: '已验收' },
              ]"
              :key="opt.key"
              :class="{ primary: localStateFilter === opt.key }"
              style="padding: 3px 8px; font-size: 12px"
              @click="localStateFilter = opt.key"
            >
              {{ opt.label }}
            </button>
          </div>
          <button class="primary" :disabled="loading" @click="queryJira">
            {{ loading ? '查询中…' : '查询 Jira 工单' }}
          </button>
        </div>

        <div v-if="filteredJiraIssues.length === 0" class="muted" style="text-align: center; padding: 24px 0">
          暂无工单结果，请调整筛选条件后点击「查询 Jira 工单」。
        </div>

        <div v-for="issue in filteredJiraIssues" :key="issue.key" class="item-card stack">
          <div class="row" style="justify-content: space-between; flex-wrap: wrap">
            <div class="row" style="flex-wrap: wrap">
              <a
                v-if="issue.web_url"
                :href="issue.web_url"
                target="_blank"
                rel="noopener"
                class="remote-key"
              >
                {{ issue.key }}
              </a>
              <code v-else>{{ issue.key }}</code>
              <strong>{{ issue.title }}</strong>
            </div>
            <div class="row">
              <span class="badge">远端：{{ issue.status || '-' }}</span>
              <span v-if="issue.assignee" class="badge">指派：{{ issue.assignee }}</span>
              <span
                class="badge"
                :style="{ color: localStateMeta[issue.local_state]?.color || 'var(--accent-cyan)' }"
              >
                本地：{{ localStateMeta[issue.local_state]?.label || issue.local_state }}
              </span>
            </div>
          </div>

          <div class="action-bar">
            <div class="row" style="flex: 1; flex-wrap: wrap">
              <select
                v-if="followupDrafts[issue.key]"
                v-model="followupDrafts[issue.key].state"
                style="width: 130px"
              >
                <option value="active">跟踪中 (active)</option>
                <option value="paused">本地搁置 (paused)</option>
                <option value="pending_retest">待复测 (pending_retest)</option>
                <option value="verified">已验收 (verified)</option>
              </select>
              <input
                v-if="followupDrafts[issue.key]"
                v-model="followupDrafts[issue.key].reason"
                placeholder="本地备注 / 搁置原因（仅本地留存，不回写 Jira）"
                style="flex: 1; min-width: 180px"
              />
              <button @click="saveFollowup(issue)">保存本地状态</button>
            </div>

            <div class="row" style="flex-wrap: wrap">
              <select v-model="linkTaskMap[issue.key]" style="width: 190px">
                <option value="" disabled>选择目标任务…</option>
                <option v-for="t in tasks" :key="t.id" :value="t.id">{{ t.title }}</option>
              </select>
              <button class="primary" :disabled="!tasks.length" @click="linkJiraIssue(issue)">
                关联到任务
              </button>
            </div>
          </div>
        </div>
      </template>
    </section>

    <!-- CodeHost (GitLab / GitHub) MR / PR Sub-tab -->
    <section v-else class="card stack">
      <div v-if="gitlabConns.length === 0" class="stack" style="align-items: center; padding: 32px 12px">
        <p class="muted" style="margin: 0">尚未配置已启用的 GitLab / GitHub 连接实例。</p>
        <button class="primary" @click="emit('open-settings')">前往「配置与组件 → Git 与代码托管」添加连接</button>
      </div>

      <template v-else>
        <div class="filter-grid">
          <label class="field">
            <span>代码托管实例</span>
            <select v-model="selectedGitLabId">
              <option v-for="c in gitlabConns" :key="c.id" :value="c.id">
                [{{ c.driver }}] {{ c.name }} ({{ c.base_url }})
              </option>
            </select>
          </label>
          <label class="field">
            <span>仓库路径（可选，如 owner/repo 或 group/repo）</span>
            <input
              v-model="gitlabFilter.project"
              list="gitlab-projects-datalist"
              placeholder="留空查与我相关的全部 MR / PR"
            />
            <datalist id="gitlab-projects-datalist">
              <option
                v-for="p in gitlabProjects"
                :key="p.id"
                :value="p.path_with_namespace"
              />
            </datalist>
          </label>
          <label class="field">
            <span>范围 / 状态</span>
            <div class="row">
              <select v-model="gitlabFilter.scope">
                <option value="all">全部范围</option>
                <option value="assigned_to_me">指派给我</option>
                <option value="created_by_me">由我创建</option>
              </select>
              <select v-model="gitlabFilter.state">
                <option value="opened">opened / open</option>
                <option value="merged">merged</option>
                <option value="closed">closed</option>
                <option value="all">all</option>
              </select>
            </div>
          </label>
          <label class="field">
            <span>关键词</span>
            <input v-model="gitlabFilter.q" placeholder="搜索 MR / PR 标题…" @keyup.enter="queryGitLabMRs" />
          </label>
        </div>

        <div class="row" style="justify-content: flex-end">
          <button class="ghost" @click="loadGitLabProjects">载入仓库建议</button>
          <button class="primary" :disabled="loading" @click="queryGitLabMRs">
            {{ loading ? '查询中…' : '查询 MR / PR' }}
          </button>
        </div>

        <div v-if="gitlabMRs.length === 0" class="muted" style="text-align: center; padding: 24px 0">
          暂无 MR / PR 结果，请点击「查询 MR / PR」。
        </div>

        <div v-for="mr in gitlabMRs" :key="mr.id" class="item-card stack">
          <div class="row" style="justify-content: space-between; flex-wrap: wrap">
            <div class="row" style="flex-wrap: wrap">
              <a
                v-if="mr.web_url"
                :href="mr.web_url"
                target="_blank"
                rel="noopener"
                class="remote-key"
              >
                {{ mr.id.includes('#') || mr.id.includes('!') ? mr.id : `!${mr.id}` }}
              </a>
              <code v-else>{{ mr.id.includes('#') || mr.id.includes('!') ? mr.id : `!${mr.id}` }}</code>
              <strong>{{ mr.title }}</strong>
            </div>
            <div class="row">
              <span class="badge">{{ mr.state }}</span>
              <span v-if="mr.source_branch || mr.target_branch" class="badge">
                <code>{{ mr.source_branch }}</code> → <code>{{ mr.target_branch }}</code>
              </span>
            </div>
          </div>

          <div class="row" style="justify-content: flex-end; flex-wrap: wrap">
            <select v-model="linkTaskMap[`mr:${mr.id}`]" style="width: 220px">
              <option value="" disabled>选择目标任务…</option>
              <option v-for="t in tasks" :key="t.id" :value="t.id">{{ t.title }}</option>
            </select>
            <button class="primary" :disabled="!tasks.length" @click="linkMR(mr)">
              关联到任务
            </button>
          </div>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.filter-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 10px;
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
.item-card {
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 12px;
  background: rgba(7, 11, 20, 0.45);
}
.remote-key {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-weight: 600;
  color: var(--accent-cyan);
  text-decoration: none;
}
.remote-key:hover {
  text-decoration: underline;
}
.action-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  border-top: 1px solid var(--border-subtle);
  padding-top: 10px;
}
@media (max-width: 960px) {
  .filter-grid {
    grid-template-columns: 1fr;
  }
}
</style>
