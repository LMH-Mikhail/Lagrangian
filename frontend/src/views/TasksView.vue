<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { api, attachmentAccessURL } from '../api/client'
import type {
  AppConfig,
  CommitCandidate,
  GitLabRemoteCommit,
  IntegrationConnection,
  ReconcileResult,
  Task,
  TaskDetail,
} from '../types/models'

const props = defineProps<{
  config: AppConfig
  initialTaskId?: string
}>()

const emit = defineEmits<{
  (e: 'changed'): void
}>()

const tasks = ref<Task[]>([])
const selectedId = ref<string>(props.initialTaskId || '')
const detail = ref<TaskDetail | null>(null)
const statusFilter = ref('')
const searchQuery = ref('')
const includeArchived = ref(false)
const errorMsg = ref('')
const noticeMsg = ref('')

const showNewTask = ref(false)
const newTask = reactive({
  title: '',
  description: '',
  status: '',
  priority: 'medium' as Task['priority'],
  due_date: '',
  next_action: '',
  tagsText: '',
})

const progressForm = reactive({
  title: '',
  body: '',
  occurred_on: new Date().toISOString().slice(0, 10),
})
const pendingFiles = ref<File[]>([])

const repoForm = reactive({
  repo_name: '',
  repo_path: '',
  branch_name: '',
})

const showCandidates = ref(false)
const candidates = ref<CommitCandidate[]>([])
const candidateWarnings = ref<string[]>([])
const selectedSHAs = ref<string[]>([])
const reconcileResult = ref<ReconcileResult | null>(null)

// Phase 2: Remote GitLab commit lookup & binding
const gitlabConns = ref<IntegrationConnection[]>([])
const showRemoteCommits = ref(false)
const remoteForm = reactive({
  connection_id: '',
  project: '',
  branch: '',
})
const remoteCommits = ref<GitLabRemoteCommit[]>([])
const selectedRemoteSHAs = ref<string[]>([])
const loadingRemoteCommits = ref(false)

const statusMap = computed(() => {
  const m = new Map<string, { label: string; color: string }>()
  for (const s of props.config.workflow.task_statuses) {
    m.set(s.key, { label: s.label, color: s.color })
  }
  return m
})

watch(
  () => props.initialTaskId,
  (id) => {
    if (id) {
      selectedId.value = id
      loadDetail(id)
    }
  },
)

onMounted(async () => {
  newTask.status = props.config.workflow.task_statuses[0]?.key || 'backlog'
  await Promise.all([loadTasks(), loadGitLabConnections()])
  if (selectedId.value) {
    await loadDetail(selectedId.value)
  } else if (tasks.value.length > 0) {
    selectedId.value = tasks.value[0].id
    await loadDetail(selectedId.value)
  }
})

async function loadGitLabConnections() {
  try {
    const res = await api.listConnections('code_host')
    gitlabConns.value = res.connections.filter(
      (c) => c.enabled && (c.driver === 'gitlab' || c.driver === 'github'),
    )
    if (!remoteForm.connection_id && gitlabConns.value.length > 0) {
      remoteForm.connection_id = gitlabConns.value[0].id
    }
  } catch {
    // non-fatal
  }
}

async function loadTasks() {
  errorMsg.value = ''
  try {
    const res = await api.listTasks({
      status: statusFilter.value,
      q: searchQuery.value,
      include_archived: includeArchived.value,
    })
    tasks.value = res.tasks
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '加载任务列表失败'
  }
}

async function loadDetail(id: string) {
  if (!id) {
    detail.value = null
    return
  }
  errorMsg.value = ''
  try {
    detail.value = await api.getTaskDetail(id)
    showCandidates.value = false
    reconcileResult.value = null
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '加载任务详情失败'
  }
}

async function selectTask(id: string) {
  selectedId.value = id
  await loadDetail(id)
}

async function createTask() {
  if (!newTask.title.trim()) return
  errorMsg.value = ''
  try {
    const created = await api.createTask({
      title: newTask.title,
      description: newTask.description,
      status: newTask.status || props.config.workflow.task_statuses[0]?.key,
      priority: newTask.priority,
      due_date: newTask.due_date,
      next_action: newTask.next_action,
      tags: newTask.tagsText
        .split(/[,，]/)
        .map((s) => s.trim())
        .filter(Boolean),
    })
    newTask.title = ''
    newTask.description = ''
    newTask.due_date = ''
    newTask.next_action = ''
    newTask.tagsText = ''
    showNewTask.value = false
    await loadTasks()
    await selectTask(created.id)
    emit('changed')
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '创建任务失败'
  }
}

async function patchCurrentTask(patch: Partial<Task>) {
  if (!detail.value) return
  errorMsg.value = ''
  try {
    await api.updateTask(detail.value.task.id, patch)
    await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
    emit('changed')
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '更新任务失败'
  }
}

async function removeCurrentTask() {
  if (!detail.value) return
  if (!confirm(`确认删除任务「${detail.value.task.title}」及其关联记录？`)) return
  await api.deleteTask(detail.value.task.id)
  detail.value = null
  selectedId.value = ''
  await loadTasks()
  emit('changed')
}

function onFileSelect(ev: Event) {
  const input = ev.target as HTMLInputElement
  if (input.files) {
    pendingFiles.value.push(...Array.from(input.files))
    input.value = ''
  }
}

function onPasteProgress(ev: ClipboardEvent) {
  const items = ev.clipboardData?.items
  if (!items) return
  for (const item of Array.from(items)) {
    if (item.kind === 'file') {
      const f = item.getAsFile()
      if (f) pendingFiles.value.push(f)
    }
  }
}

async function submitProgress() {
  if (!detail.value) return
  if (!progressForm.title.trim() && !progressForm.body.trim()) return
  errorMsg.value = ''
  try {
    const entry = await api.addProgress(detail.value.task.id, {
      title: progressForm.title,
      body: progressForm.body,
      occurred_on: progressForm.occurred_on,
    })
    for (const f of pendingFiles.value) {
      await api.uploadAttachment(detail.value.task.id, f, entry.id)
    }
    progressForm.title = ''
    progressForm.body = ''
    pendingFiles.value = []
    await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
    emit('changed')
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '保存进展失败'
  }
}

async function deleteProgress(progressId: string) {
  if (!detail.value) return
  await api.deleteProgress(detail.value.task.id, progressId)
  await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
  emit('changed')
}

async function bindRepo() {
  if (!detail.value || !repoForm.branch_name.trim()) return
  errorMsg.value = ''
  try {
    await api.bindRepo(detail.value.task.id, {
      repo_name: repoForm.repo_name,
      repo_path: repoForm.repo_path,
      branch_name: repoForm.branch_name,
    })
    repoForm.repo_name = ''
    repoForm.repo_path = ''
    repoForm.branch_name = ''
    await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
    emit('changed')
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '绑定仓库失败'
  }
}

async function unbindRepo(repoId: string) {
  if (!detail.value) return
  await api.unbindRepo(detail.value.task.id, repoId)
  await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
  emit('changed')
}

async function loadCommitCandidates() {
  if (!detail.value) return
  errorMsg.value = ''
  try {
    const res = await api.commitCandidates(detail.value.task.id, 60)
    candidates.value = res.candidates
    candidateWarnings.value = res.warnings
    selectedSHAs.value = []
    showCandidates.value = true
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '读取提交候选失败'
  }
}

async function bindSelectedCommits() {
  if (!detail.value || selectedSHAs.value.length === 0) return
  const picked = candidates.value.filter((c) => selectedSHAs.value.includes(c.sha))
  const res = await api.bindCommits(detail.value.task.id, picked)
  noticeMsg.value = `已绑定 ${res.bound.length} 条提交` + (res.skipped.length ? `，跳过 ${res.skipped.length} 条` : '')
  showCandidates.value = false
  await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
  emit('changed')
}

async function unbindCommit(commitId: string) {
  if (!detail.value) return
  await api.unbindCommit(detail.value.task.id, commitId)
  await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
  emit('changed')
}

async function runRebaseReconcile() {
  if (!detail.value) return
  errorMsg.value = ''
  reconcileResult.value = await api.reconcileRebases(detail.value.task.id)
  await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
  emit('changed')
}

async function unlinkRemote(linkId: string) {
  if (!detail.value) return
  errorMsg.value = ''
  try {
    await api.unlinkRemote(detail.value.task.id, linkId)
    await loadDetail(detail.value.task.id)
    emit('changed')
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '解除外部关联失败'
  }
}

async function openRemoteCommits() {
  showRemoteCommits.value = !showRemoteCommits.value
  if (showRemoteCommits.value && gitlabConns.value.length === 0) {
    await loadGitLabConnections()
  }
}

async function queryRemoteCommits() {
  if (!remoteForm.connection_id || !remoteForm.project.trim() || !remoteForm.branch.trim()) {
    errorMsg.value = '请选择 GitLab 连接并填写项目路径与分支名。'
    return
  }
  loadingRemoteCommits.value = true
  errorMsg.value = ''
  try {
    const res = await api.gitlabCommits(
      remoteForm.connection_id,
      remoteForm.project.trim(),
      remoteForm.branch.trim(),
    )
    remoteCommits.value = res.commits
    selectedRemoteSHAs.value = []
    if (res.commits.length === 0) {
      noticeMsg.value = '远端分支及已合并 MR 中未找到符合作者别名的提交。'
    }
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '查询远端 GitLab 提交失败'
  } finally {
    loadingRemoteCommits.value = false
  }
}

async function bindSelectedRemoteCommits() {
  if (!detail.value || selectedRemoteSHAs.value.length === 0) return
  errorMsg.value = ''
  try {
    const activeConn = gitlabConns.value.find((c) => c.id === remoteForm.connection_id)
    const src = activeConn ? `${activeConn.driver}_remote` : 'gitlab_remote'
    const picked = remoteCommits.value
      .filter((c) => selectedRemoteSHAs.value.includes(c.sha))
      .map((c) => ({
        repo_name: remoteForm.project.trim(),
        branch_name: remoteForm.branch.trim(),
        sha: c.sha,
        title: c.title,
        author: c.author,
        committed_at: c.committed_at,
        source: src,
      }))
    const res = await api.bindCommits(detail.value.task.id, picked)
    noticeMsg.value =
      `已绑定 ${res.bound.length} 条远端提交` +
      (res.skipped.length ? `，跳过 ${res.skipped.length} 条` : '')
    showRemoteCommits.value = false
    remoteCommits.value = []
    selectedRemoteSHAs.value = []
    await Promise.all([loadTasks(), loadDetail(detail.value.task.id)])
    emit('changed')
  } catch (err) {
    errorMsg.value = err instanceof Error ? err.message : '绑定远端提交失败'
  }
}
</script>

<template>
  <div class="tasks-layout">
    <section class="card stack list-col">
      <div class="row" style="justify-content: space-between">
        <strong>任务台账 ({{ tasks.length }})</strong>
        <button class="primary" @click="showNewTask = !showNewTask">
          {{ showNewTask ? '收起' : '+ 新建任务' }}
        </button>
      </div>

      <div v-if="showNewTask" class="new-task-box stack">
        <input v-model="newTask.title" placeholder="任务标题（必填）" />
        <textarea v-model="newTask.description" rows="2" placeholder="任务描述（可选）"></textarea>
        <div class="row">
          <select v-model="newTask.status">
            <option v-for="st in config.workflow.task_statuses" :key="st.key" :value="st.key">
              {{ st.label }}
            </option>
          </select>
          <select v-model="newTask.priority">
            <option value="low">低优先级</option>
            <option value="medium">中优先级</option>
            <option value="high">高优先级</option>
            <option value="urgent">紧急</option>
          </select>
        </div>
        <div class="row">
          <input v-model="newTask.due_date" type="date" title="截止日期" />
          <input v-model="newTask.tagsText" placeholder="标签，逗号分隔（可选）" />
        </div>
        <input v-model="newTask.next_action" placeholder="下一步行动（可选）" />
        <div class="row" style="justify-content: flex-end">
          <button class="primary" @click="createTask">创建</button>
        </div>
      </div>

      <div class="row">
        <input
          v-model="searchQuery"
          placeholder="搜索标题、描述、下一步…"
          @keyup.enter="loadTasks"
        />
        <select v-model="statusFilter" style="width: 120px" @change="loadTasks">
          <option value="">全部状态</option>
          <option v-for="st in config.workflow.task_statuses" :key="st.key" :value="st.key">
            {{ st.label }}
          </option>
        </select>
      </div>

      <label class="row muted" style="font-size: 12px; cursor: pointer">
        <input v-model="includeArchived" type="checkbox" style="width: auto" @change="loadTasks" />
        <span>包含已归档任务</span>
      </label>

      <div class="task-list stack">
        <div
          v-for="t in tasks"
          :key="t.id"
          class="task-item"
          :class="{ selected: t.id === selectedId }"
          @click="selectTask(t.id)"
        >
          <div class="row" style="justify-content: space-between">
            <span class="task-title">{{ t.title }}</span>
            <span class="badge">
              <span
                class="dot"
                :style="{ background: statusMap.get(t.status)?.color || '#64748b' }"
              ></span>
              {{ statusMap.get(t.status)?.label || t.status }}
            </span>
          </div>
          <div v-if="t.next_action" class="muted" style="font-size: 12px">
            下一步：{{ t.next_action }}
          </div>
          <div class="row muted" style="font-size: 11px">
            <span>进展 {{ t.progress_count || 0 }}</span>
            <span>· 分支 {{ t.repo_count || 0 }}</span>
            <span>· 提交 {{ t.commit_count || 0 }}</span>
            <span v-if="t.blocker" style="color: var(--accent-rose)">· 有阻塞</span>
          </div>
        </div>

        <p v-if="tasks.length === 0" class="muted" style="text-align: center; padding: 24px 0">
          暂无任务，点击右上角「+ 新建任务」开始记录。
        </p>
      </div>
    </section>

    <section class="stack detail-col">
      <div v-if="errorMsg" class="error-banner">{{ errorMsg }}</div>
      <div v-if="noticeMsg" class="notice-banner row" style="justify-content: space-between">
        <span>{{ noticeMsg }}</span>
        <button class="ghost" @click="noticeMsg = ''">关闭</button>
      </div>

      <template v-if="detail">
        <div class="card stack">
          <div class="row" style="justify-content: space-between; align-items: flex-start; gap: 12px">
            <div style="flex: 1" class="stack">
              <input
                class="title-input"
                :value="detail.task.title"
                @change="patchCurrentTask({ title: ($event.target as HTMLInputElement).value })"
              />
              <div class="row" style="flex-wrap: wrap">
                <button
                  v-for="st in config.workflow.task_statuses"
                  :key="st.key"
                  :class="{ primary: detail.task.status === st.key }"
                  style="padding: 3px 9px; font-size: 12px"
                  @click="patchCurrentTask({ status: st.key })"
                >
                  {{ st.label }}
                </button>
              </div>
            </div>
            <div class="row">
              <button @click="patchCurrentTask({ archived: !detail.task.archived })">
                {{ detail.task.archived ? '恢复归档' : '归档' }}
              </button>
              <button class="danger" @click="removeCurrentTask">删除</button>
            </div>
          </div>

          <div class="grid-3">
            <label class="field">
              <span>优先级</span>
              <select
                :value="detail.task.priority"
                @change="
                  patchCurrentTask({
                    priority: ($event.target as HTMLSelectElement).value as Task['priority'],
                  })
                "
              >
                <option value="low">低优先级</option>
                <option value="medium">中优先级</option>
                <option value="high">高优先级</option>
                <option value="urgent">紧急</option>
              </select>
            </label>
            <label class="field">
              <span>截止日期</span>
              <input
                type="date"
                :value="detail.task.due_date"
                @change="patchCurrentTask({ due_date: ($event.target as HTMLInputElement).value })"
              />
            </label>
            <label class="field">
              <span>标签（逗号分隔）</span>
              <input
                :value="(detail.task.tags || []).join(', ')"
                placeholder="失焦自动保存"
                @change="
                  patchCurrentTask({
                    tags: ($event.target as HTMLInputElement).value
                      .split(/[,，]/)
                      .map((s) => s.trim())
                      .filter(Boolean),
                  })
                "
              />
            </label>
          </div>

          <label class="field">
            <span>任务描述</span>
            <textarea
              rows="2"
              :value="detail.task.description"
              placeholder="补充任务背景、目标或验收条件（失焦自动保存）"
              @change="patchCurrentTask({ description: ($event.target as HTMLTextAreaElement).value })"
            ></textarea>
          </label>

          <div class="grid-3">
            <label class="field">
              <span>下一步行动</span>
              <input
                :value="detail.task.next_action"
                placeholder="失焦自动保存"
                @change="patchCurrentTask({ next_action: ($event.target as HTMLInputElement).value })"
              />
            </label>
            <label class="field">
              <span>阻塞与等待</span>
              <input
                :value="detail.task.blocker"
                placeholder="无阻塞留空"
                @change="patchCurrentTask({ blocker: ($event.target as HTMLInputElement).value })"
              />
            </label>
            <label class="field">
              <span>恢复条件</span>
              <input
                :value="detail.task.resume_condition"
                placeholder="解除阻塞的条件"
                @change="patchCurrentTask({ resume_condition: ($event.target as HTMLInputElement).value })"
              />
            </label>
          </div>
        </div>

        <div class="card stack">
          <strong>工作进展与证据附件</strong>
          <div class="stack progress-editor" @paste="onPasteProgress">
            <div class="grid-2">
              <input v-model="progressForm.title" placeholder="进展标题（留空默认取正文首行）" />
              <input v-model="progressForm.occurred_on" type="date" />
            </div>
            <textarea
              v-model="progressForm.body"
              rows="3"
              placeholder="记录进展正文，支持直接 Ctrl/Cmd+V 粘贴截图作为附件…"
            ></textarea>
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <div class="row">
                <label class="file-btn">
                  选择附件（图片/HTML/文件）
                  <input type="file" multiple hidden @change="onFileSelect" />
                </label>
                <span v-if="pendingFiles.length" class="muted">
                  待上传 {{ pendingFiles.length }} 个文件
                </span>
              </div>
              <button class="primary" @click="submitProgress">保存进展</button>
            </div>
          </div>

          <div v-for="p in detail.progress" :key="p.id" class="progress-item stack">
            <div class="row" style="justify-content: space-between">
              <div class="row">
                <strong>{{ p.title }}</strong>
                <span class="badge">{{ p.occurred_on }}</span>
              </div>
              <button class="ghost danger" @click="deleteProgress(p.id)">删除</button>
            </div>
            <p v-if="p.body" class="progress-body">{{ p.body }}</p>
            <div v-if="p.attachments?.length" class="row" style="flex-wrap: wrap">
              <a
                v-for="a in p.attachments"
                :key="a.id"
                :href="attachmentAccessURL(a.download_url)"
                target="_blank"
                rel="noopener"
                class="att-chip"
              >
                <img
                  v-if="a.mime_type.startsWith('image/')"
                  :src="attachmentAccessURL(a.download_url)"
                  :alt="a.filename"
                  class="att-thumb"
                />
                <span>{{ a.filename }}</span>
              </a>
            </div>
          </div>
        </div>

        <div v-if="detail.remote_links?.length" class="card stack">
          <strong>关联外部工单与 Merge Request ({{ detail.remote_links.length }})</strong>
          <div v-for="rl in detail.remote_links" :key="rl.id" class="row commit-row">
            <span class="badge">{{ rl.remote_kind === 'issue' ? 'Jira' : 'MR' }}</span>
            <a
              v-if="rl.web_url"
              :href="rl.web_url"
              target="_blank"
              rel="noopener"
              style="color: var(--accent-cyan); text-decoration: none; font-weight: 600"
            >
              {{ rl.remote_key }}
            </a>
            <code v-else>{{ rl.remote_key }}</code>
            <span style="flex: 1">{{ rl.title }}</span>
            <span class="badge">{{ rl.remote_state || '-' }}</span>
            <button class="ghost danger" @click="unlinkRemote(rl.id)">解绑</button>
          </div>
        </div>

        <div class="card stack">
          <div class="row" style="justify-content: space-between; flex-wrap: wrap">
            <strong>关联仓库分支与 Git 提交 ({{ detail.commits.length }})</strong>
            <div class="row">
              <button @click="loadCommitCandidates">绑定本地提交</button>
              <button v-if="gitlabConns.length" @click="openRemoteCommits">
                {{ showRemoteCommits ? '收起远端拉取' : '拉取远端提交 (GitLab/GitHub)' }}
              </button>
              <button @click="runRebaseReconcile">核验 Rebase 归并</button>
            </div>
          </div>

          <div v-if="showRemoteCommits" class="candidates-box stack">
            <div class="row" style="justify-content: space-between; flex-wrap: wrap">
              <strong>从远端 GitLab / GitHub 拉取分支或已合并 MR / PR 提交</strong>
              <button
                v-if="remoteCommits.length"
                class="primary"
                :disabled="!selectedRemoteSHAs.length"
                @click="bindSelectedRemoteCommits"
              >
                绑定已选远端提交 ({{ selectedRemoteSHAs.length }})
              </button>
            </div>
            <div class="grid-3">
              <select v-model="remoteForm.connection_id">
                <option v-for="c in gitlabConns" :key="c.id" :value="c.id">
                  [{{ c.driver }}] {{ c.name }}
                </option>
              </select>
              <input v-model="remoteForm.project" placeholder="仓库路径，如 owner/repo 或 group/repo" />
              <div class="row">
                <input v-model="remoteForm.branch" placeholder="远端分支名（支持已删分支 MR/PR 回退）" />
                <button
                  class="primary"
                  style="white-space: nowrap"
                  :disabled="loadingRemoteCommits"
                  @click="queryRemoteCommits"
                >
                  {{ loadingRemoteCommits ? '查询中…' : '查询' }}
                </button>
              </div>
            </div>
            <label v-for="rc in remoteCommits" :key="rc.sha" class="row cand-row">
              <input
                v-model="selectedRemoteSHAs"
                type="checkbox"
                :value="rc.sha"
                style="width: auto"
              />
              <code>{{ rc.sha.slice(0, 8) }}</code>
              <span style="flex: 1">{{ rc.title }}</span>
              <span class="muted">{{ rc.author }}</span>
            </label>
          </div>

          <div class="grid-3">
            <input v-model="repoForm.repo_name" placeholder="仓库名（可留空自动取目录名）" />
            <input v-model="repoForm.repo_path" placeholder="本机 Git 仓库绝对路径" />
            <div class="row">
              <input v-model="repoForm.branch_name" placeholder="分支名" />
              <button @click="bindRepo" style="white-space: nowrap">绑定分支</button>
            </div>
          </div>

          <div v-if="detail.repos.length" class="row" style="flex-wrap: wrap">
            <span v-for="r in detail.repos" :key="r.id" class="badge">
              {{ r.repo_name }} @ {{ r.branch_name }}
              <button class="ghost" style="padding: 0 4px" @click="unbindRepo(r.id)">×</button>
            </span>
          </div>

          <div v-if="reconcileResult" class="notice-banner">
            扫描 {{ reconcileResult.scanned_branches }} 条分支，归并
            {{ reconcileResult.replaced.length }} 条被 rebase 替代的提交。
          </div>

          <div v-if="showCandidates" class="candidates-box stack">
            <div class="row" style="justify-content: space-between">
              <strong>可选提交候选（已按作者别名过滤）</strong>
              <div class="row">
                <button class="primary" :disabled="!selectedSHAs.length" @click="bindSelectedCommits">
                  确认绑定 ({{ selectedSHAs.length }})
                </button>
                <button @click="showCandidates = false">关闭</button>
              </div>
            </div>
            <div v-for="w in candidateWarnings" :key="w" class="muted">{{ w }}</div>
            <label v-for="c in candidates" :key="c.sha" class="row cand-row">
              <input
                v-model="selectedSHAs"
                type="checkbox"
                :value="c.sha"
                :disabled="c.already_bound || c.superseded"
                style="width: auto"
              />
              <code>{{ c.sha.slice(0, 8) }}</code>
              <span style="flex: 1">{{ c.title }}</span>
              <span class="muted">{{ c.repo_name }}/{{ c.branch_name }}</span>
              <span v-if="c.superseded" class="badge">已归并</span>
              <span v-else-if="c.already_bound" class="badge">已绑定</span>
            </label>
          </div>

          <div v-for="c in detail.commits" :key="c.id" class="row commit-row">
            <code>{{ c.sha.slice(0, 8) }}</code>
            <span style="flex: 1">{{ c.title }}</span>
            <span class="badge">{{ c.repo_name }}</span>
            <span class="muted" style="font-size: 11px">{{ c.source }}</span>
            <button class="ghost danger" @click="unbindCommit(c.id)">解绑</button>
          </div>
        </div>
      </template>

      <div v-else class="card muted" style="text-align: center; padding: 48px">
        请从左侧选择任务或新建任务。
      </div>
    </section>
  </div>
</template>

<style scoped>
.tasks-layout {
  display: grid;
  grid-template-columns: 340px 1fr;
  gap: 14px;
  align-items: start;
}
.new-task-box,
.progress-editor,
.candidates-box {
  background: rgba(7, 11, 20, 0.45);
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 10px;
}
.task-item {
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 10px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.task-item:hover,
.task-item.selected {
  border-color: var(--accent-cyan);
  background: rgba(56, 189, 248, 0.08);
}
.task-title {
  font-weight: 500;
}
.title-input {
  font-size: 17px;
  font-weight: 600;
  background: transparent;
  border-color: transparent;
  padding: 2px 6px;
  margin-left: -6px;
}
.title-input:hover,
.title-input:focus {
  background: rgba(7, 11, 20, 0.72);
  border-color: var(--accent-cyan);
}
.grid-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}
.grid-3 {
  display: grid;
  grid-template-columns: 160px 1fr 1fr;
  gap: 8px;
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
.progress-item {
  border-top: 1px solid var(--border-subtle);
  padding-top: 10px;
}
.progress-body {
  margin: 0;
  white-space: pre-wrap;
  color: #cbd5e1;
}
.file-btn {
  display: inline-block;
  border: 1px dashed var(--border-strong);
  border-radius: 7px;
  padding: 5px 10px;
  cursor: pointer;
  font-size: 12px;
}
.att-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 8px;
  border: 1px solid var(--border-subtle);
  border-radius: 6px;
  color: var(--accent-cyan);
  text-decoration: none;
}
.att-thumb {
  width: 32px;
  height: 32px;
  object-fit: cover;
  border-radius: 4px;
}
.cand-row,
.commit-row {
  padding: 5px 0;
  border-top: 1px solid var(--border-subtle);
}
.notice-banner {
  background: rgba(52, 211, 153, 0.12);
  border: 1px solid rgba(52, 211, 153, 0.35);
  color: #a7f3d0;
  border-radius: 8px;
  padding: 8px 12px;
}
@media (max-width: 960px) {
  .tasks-layout,
  .grid-2,
  .grid-3 {
    grid-template-columns: 1fr;
  }
}
</style>
