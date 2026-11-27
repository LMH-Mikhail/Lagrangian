<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { api } from '../api/client'
import KnowledgeGraphView from '../components/KnowledgeGraphView.vue'
import type {
  KnowledgeBase,
  KnowledgeDoc,
  KnowledgeDocContent,
  KnowledgeGraph,
  KnowledgeSearchHit,
} from '../types/models'
import { renderKnowledgeMarkdown, resolveWikiTarget } from '../utils/markdown'

const emit = defineEmits<{
  (e: 'changed'): void
  (e: 'open-settings'): void
}>()

interface DirNode {
  name: string
  path: string
  dirs: Map<string, DirNode>
  files: KnowledgeDoc[]
  docCount: number
}

interface TreeRow {
  kind: 'dir' | 'file'
  key: string
  name: string
  path: string
  depth: number
  docCount?: number
  expanded?: boolean
  doc?: KnowledgeDoc
}

type WorkspaceMode = 'doc' | 'graph'
type DocViewMode = 'read' | 'split' | 'edit'

const bases = ref<KnowledgeBase[]>([])
const selectedKBId = ref('')
const docs = ref<KnowledgeDoc[]>([])
const activeDoc = ref<KnowledgeDocContent | null>(null)
const editorContent = ref('')
const dirty = ref(false)

const workspaceMode = ref<WorkspaceMode>('doc')
const docViewMode = ref<DocViewMode>('read')
const showToc = ref(true)
const showFrontmatter = ref(true)
const previewPaneRef = ref<HTMLElement | null>(null)

const kbGraph = ref<KnowledgeGraph | null>(null)
const loadingGraph = ref(false)

const treeFilter = ref('')
const expandedDirs = ref<Set<string>>(new Set())

const searchQuery = ref('')
const searchKBScope = ref('')
const searchHits = ref<KnowledgeSearchHit[]>([])
const hasSearched = ref(false)

const showNewDoc = ref(false)
const newDocForm = reactive({
  path: '',
  content: '# 新文档\n\n',
})

const syncing = ref(false)
const saving = ref(false)
const errMsg = ref('')
const noticeMsg = ref('')

const activeKB = computed(() =>
  bases.value.find((kb) => kb.id === selectedKBId.value) || null,
)

const canEditActiveKB = computed(
  () => !!activeKB.value && !activeKB.value.readonly,
)

const renderedDoc = computed(() => {
  if (!activeDoc.value) {
    return { frontmatter: [], html: '', toc: [] }
  }
  const sourceText = canEditActiveKB.value ? editorContent.value : activeDoc.value.content
  return renderKnowledgeMarkdown(sourceText, activeDoc.value.doc.rel_path, docs.value)
})

const activeBacklinks = computed(() => {
  if (!activeDoc.value || !kbGraph.value) return []
  const target = activeDoc.value.doc.rel_path
  const nodeMap = new Map(kbGraph.value.nodes.map((n) => [n.id, n]))
  const out: Array<{ relPath: string; title: string }> = []
  for (const e of kbGraph.value.edges) {
    if (e.target === target) {
      const n = nodeMap.get(e.source)
      out.push({ relPath: e.source, title: n?.title || e.source })
    }
  }
  return out
})

const activeBreadcrumbs = computed(() => {
  if (!activeDoc.value) return []
  const parts = activeDoc.value.doc.rel_path.split('/').filter(Boolean)
  const crumbs: Array<{ label: string; dirPath: string; isFile: boolean }> = []
  let acc = ''
  parts.forEach((seg, idx) => {
    acc = acc ? `${acc}/${seg}` : seg
    crumbs.push({
      label: seg,
      dirPath: acc,
      isFile: idx === parts.length - 1,
    })
  })
  return crumbs
})

function naturalCompare(a: string, b: string): number {
  return a.localeCompare(b, 'zh-CN', { numeric: true, sensitivity: 'base' })
}

const filteredDocs = computed(() => {
  const q = treeFilter.value.trim().toLowerCase()
  if (!q) return docs.value
  return docs.value.filter(
    (d) =>
      d.rel_path.toLowerCase().includes(q) ||
      d.title.toLowerCase().includes(q),
  )
})

const allDirPaths = computed(() => {
  const dirs = new Set<string>()
  for (const d of docs.value) {
    const parts = d.rel_path.split('/').filter(Boolean)
    let acc = ''
    for (let i = 0; i < parts.length - 1; i++) {
      acc = acc ? `${acc}/${parts[i]}` : parts[i]
      dirs.add(acc)
    }
  }
  return dirs
})

const visibleTreeRows = computed<TreeRow[]>(() => {
  const root: DirNode = { name: '', path: '', dirs: new Map(), files: [], docCount: 0 }
  for (const doc of filteredDocs.value) {
    const parts = doc.rel_path.split('/').filter(Boolean)
    let cur = root
    cur.docCount++
    for (let i = 0; i < parts.length - 1; i++) {
      const seg = parts[i]
      const nextPath = cur.path ? `${cur.path}/${seg}` : seg
      let child = cur.dirs.get(seg)
      if (!child) {
        child = { name: seg, path: nextPath, dirs: new Map(), files: [], docCount: 0 }
        cur.dirs.set(seg, child)
      }
      child.docCount++
      cur = child
    }
    cur.files.push(doc)
  }

  const forceExpand = treeFilter.value.trim().length > 0
  const rows: TreeRow[] = []

  function walk(node: DirNode, depth: number) {
    const sortedDirs = Array.from(node.dirs.values()).sort((a, b) => naturalCompare(a.name, b.name))
    for (const dir of sortedDirs) {
      const isExpanded = forceExpand || expandedDirs.value.has(dir.path)
      rows.push({
        kind: 'dir',
        key: `dir:${dir.path}`,
        name: dir.name,
        path: dir.path,
        depth,
        docCount: dir.docCount,
        expanded: isExpanded,
      })
      if (isExpanded) {
        walk(dir, depth + 1)
      }
    }
    const sortedFiles = [...node.files].sort((a, b) => naturalCompare(a.rel_path, b.rel_path))
    for (const file of sortedFiles) {
      const segs = file.rel_path.split('/')
      const fileName = segs[segs.length - 1] || file.rel_path
      rows.push({
        kind: 'file',
        key: `file:${file.rel_path}`,
        name: file.title || fileName,
        path: file.rel_path,
        depth,
        doc: file,
      })
    }
  }

  walk(root, 0)
  return rows
})

function toggleDir(dirPath: string) {
  const next = new Set(expandedDirs.value)
  if (next.has(dirPath)) {
    next.delete(dirPath)
  } else {
    next.add(dirPath)
  }
  expandedDirs.value = next
}

function expandAncestors(relPath: string) {
  const parts = relPath.split('/').filter(Boolean)
  if (parts.length <= 1) return
  const next = new Set(expandedDirs.value)
  let acc = ''
  for (let i = 0; i < parts.length - 1; i++) {
    acc = acc ? `${acc}/${parts[i]}` : parts[i]
    next.add(acc)
  }
  expandedDirs.value = next
}

function expandAllDirs() {
  expandedDirs.value = new Set(allDirPaths.value)
}

function collapseAllDirs() {
  expandedDirs.value = new Set()
}

function openNewDocInDir(dirPath?: string) {
  if (!canEditActiveKB.value) return
  workspaceMode.value = 'doc'
  if (showNewDoc.value && dirPath === undefined) {
    showNewDoc.value = false
    return
  }
  let prefix = dirPath || ''
  if (!prefix && activeDoc.value) {
    const parts = activeDoc.value.doc.rel_path.split('/').filter(Boolean)
    if (parts.length > 1) {
      prefix = parts.slice(0, -1).join('/')
    }
  }
  newDocForm.path = prefix ? `${prefix}/` : ''
  newDocForm.content = '# 新文档\n\n'
  showNewDoc.value = true
}

function scrollToHeading(id: string) {
  const container = previewPaneRef.value
  const target = container ? container.querySelector(`#${CSS.escape(id)}`) : document.getElementById(id)
  if (target) {
    target.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }
}

function scrollToHeadingText(headingText: string) {
  if (!headingText) return
  const needle = headingText.trim().toLowerCase()
  const hit = renderedDoc.value.toc.find((t) => t.text.toLowerCase() === needle || t.text.toLowerCase().includes(needle))
  if (hit) {
    scrollToHeading(hit.id)
  }
}

async function onMarkdownClick(ev: MouseEvent) {
  const el = (ev.target as HTMLElement | null)?.closest('a') as HTMLAnchorElement | null
  if (!el || !activeKB.value) return

  const wikiTarget = el.getAttribute('data-wiki-target')
  if (wikiTarget) {
    ev.preventDefault()
    const isResolved = el.getAttribute('data-wiki-resolved') === '1'
    if (!isResolved) {
      errMsg.value = `未在当前知识库中找到目标文档「${wikiTarget}」`
      return
    }
    const [pathPart, headingPart] = wikiTarget.split('#')
    if (pathPart && pathPart !== activeDoc.value?.doc.rel_path) {
      await openDoc(activeKB.value.id, pathPart)
      if (headingPart) {
        await nextTick()
        scrollToHeadingText(headingPart)
      }
    } else if (headingPart) {
      scrollToHeadingText(headingPart)
    }
    return
  }

  const href = el.getAttribute('href') || ''
  if (!href || /^https?:\/\//i.test(href) || href.startsWith('mailto:')) {
    return
  }
  if (href.startsWith('#')) {
    ev.preventDefault()
    scrollToHeadingText(decodeURIComponent(href.slice(1)))
    return
  }
  // Relative markdown link inside KB
  ev.preventDefault()
  const resolved = resolveWikiTarget(decodeURIComponent(href), activeDoc.value?.doc.rel_path || '', docs.value)
  if (resolved.relPath) {
    await openDoc(activeKB.value.id, resolved.relPath)
    if (resolved.heading) {
      await nextTick()
      scrollToHeadingText(resolved.heading)
    }
  } else {
    errMsg.value = `未在当前知识库中找到链接目标「${href}」`
  }
}

onMounted(loadBases)

async function loadBases() {
  errMsg.value = ''
  try {
    const res = await api.listKnowledgeBases()
    bases.value = res.knowledge_bases
    if (bases.value.length > 0) {
      if (!selectedKBId.value || !bases.value.some((b) => b.id === selectedKBId.value)) {
        selectedKBId.value = bases.value[0].id
      }
      await Promise.all([loadDocs(selectedKBId.value), loadGraph(selectedKBId.value)])
    } else {
      selectedKBId.value = ''
      docs.value = []
      activeDoc.value = null
      kbGraph.value = null
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载知识库列表失败'
  }
}

async function selectKB(kbId: string) {
  selectedKBId.value = kbId
  activeDoc.value = null
  kbGraph.value = null
  showNewDoc.value = false
  dirty.value = false
  treeFilter.value = ''
  expandedDirs.value = new Set()
  docViewMode.value = 'read'
  await Promise.all([loadDocs(kbId), loadGraph(kbId)])
}

async function loadGraph(kbId: string) {
  if (!kbId) {
    kbGraph.value = null
    return
  }
  loadingGraph.value = true
  try {
    kbGraph.value = await api.getKnowledgeGraph(kbId)
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载知识库关系图谱失败'
  } finally {
    loadingGraph.value = false
  }
}

async function loadDocs(kbId: string) {
  if (!kbId) return
  errMsg.value = ''
  try {
    const res = await api.listKnowledgeDocs(kbId)
    docs.value = res.docs
    if (docs.value.length > 0 && !activeDoc.value) {
      await openDoc(kbId, docs.value[0].rel_path, false)
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '加载文档目录失败'
  }
}

async function openDoc(kbId: string, relPath: string, expandTree = true) {
  errMsg.value = ''
  noticeMsg.value = ''
  showNewDoc.value = false
  workspaceMode.value = 'doc'
  try {
    if (kbId !== selectedKBId.value) {
      selectedKBId.value = kbId
      expandedDirs.value = new Set()
      const [listRes] = await Promise.all([api.listKnowledgeDocs(kbId), loadGraph(kbId)])
      docs.value = listRes.docs
    }
    if (expandTree) {
      expandAncestors(relPath)
    }
    const res = await api.readKnowledgeDoc(kbId, relPath)
    activeDoc.value = res
    editorContent.value = res.content
    dirty.value = false
    if (!canEditActiveKB.value && docViewMode.value === 'split') {
      docViewMode.value = 'read'
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '读取文档内容失败'
  }
}

async function openFromGraph(relPath: string) {
  if (!selectedKBId.value) return
  docViewMode.value = 'read'
  await openDoc(selectedKBId.value, relPath)
}

async function saveCurrentDoc() {
  if (!activeKB.value || !activeDoc.value || !canEditActiveKB.value) return
  saving.value = true
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    const saved = await api.saveKnowledgeDoc(
      activeKB.value.id,
      activeDoc.value.doc.rel_path,
      editorContent.value,
    )
    activeDoc.value = saved
    editorContent.value = saved.content
    dirty.value = false
    await loadBases()
    noticeMsg.value =
      activeKB.value.source_type === 'remote_git'
        ? `已保存「${saved.doc.rel_path}」，自动提交并推送到远端 Git 仓库，索引与图谱已更新。`
        : `已保存「${saved.doc.rel_path}」并实时更新 FTS5 索引与关系图谱。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '保存文档失败'
  } finally {
    saving.value = false
  }
}

async function createNewDoc() {
  if (!activeKB.value || !canEditActiveKB.value) return
  let relPath = newDocForm.path.trim()
  if (!relPath || relPath.endsWith('/')) {
    errMsg.value = '请输入完整的文档相对路径与文件名，例如 notes/guide.md'
    return
  }
  if (!relPath.endsWith('.md') && !relPath.endsWith('.markdown') && !relPath.endsWith('.txt')) {
    relPath += '.md'
  }
  saving.value = true
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    const saved = await api.saveKnowledgeDoc(activeKB.value.id, relPath, newDocForm.content)
    showNewDoc.value = false
    newDocForm.path = ''
    newDocForm.content = '# 新文档\n\n'
    await loadBases()
    expandAncestors(saved.doc.rel_path)
    activeDoc.value = saved
    editorContent.value = saved.content
    dirty.value = false
    docViewMode.value = 'read'
    emit('changed')
    noticeMsg.value =
      activeKB.value.source_type === 'remote_git'
        ? `已创建文档「${saved.doc.rel_path}」，自动提交并推送到远端 Git 仓库。`
        : `已创建文档「${saved.doc.rel_path}」并加入全文检索与图谱索引。`
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '创建文档失败'
  } finally {
    saving.value = false
  }
}

async function syncCurrentKB() {
  if (!activeKB.value) return
  syncing.value = true
  errMsg.value = ''
  noticeMsg.value = ''
  try {
    const updated = await api.syncKnowledgeBase(activeKB.value.id)
    await loadBases()
    emit('changed')
    if (updated.last_error) {
      errMsg.value = `同步失败：${updated.last_error}`
    } else {
      noticeMsg.value = `知识库「${updated.name}」同步完成，共索引 ${updated.doc_count} 篇文档。`
    }
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '同步知识库失败'
  } finally {
    syncing.value = false
  }
}

async function runSearch() {
  const q = searchQuery.value.trim()
  if (!q) {
    searchHits.value = []
    hasSearched.value = false
    return
  }
  errMsg.value = ''
  try {
    const res = await api.searchKnowledge(q, searchKBScope.value, 30)
    searchHits.value = res.hits
    hasSearched.value = true
  } catch (err) {
    errMsg.value = err instanceof Error ? err.message : '全文检索失败'
  }
}

function clearSearch() {
  searchQuery.value = ''
  searchHits.value = []
  hasSearched.value = false
}
</script>

<template>
  <div class="stack">
    <!-- Top FTS5 Search Bar -->
    <div class="card stack">
      <div class="row" style="justify-content: space-between; flex-wrap: wrap">
        <div>
          <h2 style="margin: 0; font-size: 16px">通用多知识库与 FTS5 全文检索</h2>
          <span class="muted">
            统一检索所有本地与远端 Git 知识库；支持 Markdown 排版阅读、Frontmatter 属性卡、大纲 TOC、[[双链]] 跳转与每库独立关系图谱。
          </span>
        </div>
        <div class="row">
          <button
            v-if="activeKB"
            :class="{ primary: workspaceMode === 'doc' }"
            @click="workspaceMode = 'doc'"
          >
            文档阅读
          </button>
          <button
            v-if="activeKB"
            :class="{ primary: workspaceMode === 'graph' }"
            @click="workspaceMode = 'graph'"
          >
            关系图谱 ({{ kbGraph?.edges.length || 0 }})
          </button>
          <button class="ghost" @click="emit('open-settings')">管理知识库</button>
        </div>
      </div>

      <div class="row" style="flex-wrap: wrap">
        <input
          v-model="searchQuery"
          placeholder="输入关键词进行跨知识库全文检索（支持中英文）…"
          style="flex: 1; min-width: 240px"
          @keyup.enter="runSearch"
        />
        <select v-model="searchKBScope" style="width: 200px">
          <option value="">全部知识库</option>
          <option v-for="kb in bases" :key="kb.id" :value="kb.id">{{ kb.name }}</option>
        </select>
        <button class="primary" @click="runSearch">全文检索</button>
        <button v-if="hasSearched" class="ghost" @click="clearSearch">清除结果</button>
      </div>

      <div v-if="hasSearched" class="search-panel stack">
        <div class="muted" style="font-size: 12px">
          检索「{{ searchQuery }}」命中 {{ searchHits.length }} 篇文档
        </div>
        <div v-if="searchHits.length === 0" class="muted">未命中任何文档。</div>
        <div
          v-for="hit in searchHits"
          :key="hit.doc_id"
          class="hit-row"
          @click="openDoc(hit.kb_id, hit.rel_path)"
        >
          <div class="row" style="justify-content: space-between">
            <strong>{{ hit.title }}</strong>
            <div class="row">
              <span class="badge">{{ hit.kb_name }}</span>
              <code class="muted" style="font-size: 11px">{{ hit.rel_path }}</code>
            </div>
          </div>
          <div class="muted" style="font-size: 12px; margin-top: 4px">{{ hit.snippet }}</div>
        </div>
      </div>
    </div>

    <div v-if="errMsg" class="error-banner">{{ errMsg }}</div>
    <div v-if="noticeMsg" class="card" style="border-color: var(--accent-emerald)">{{ noticeMsg }}</div>

    <!-- Empty State -->
    <div v-if="bases.length === 0" class="card stack" style="align-items: center; padding: 48px 16px">
      <p class="muted" style="margin: 0">尚未挂载任何知识库。</p>
      <button class="primary" @click="emit('open-settings')">
        前往「配置与组件 → 通用知识库」添加本地目录或远端 Git 仓库
      </button>
    </div>

    <!-- Main Two-Column Directory Tree + Reader/Editor or Graph -->
    <div v-else class="kb-layout">
      <aside class="card stack sidebar-col">
        <div class="row">
          <select
            :value="selectedKBId"
            style="flex: 1; min-width: 0"
            @change="selectKB(($event.target as HTMLSelectElement).value)"
          >
            <option v-for="kb in bases" :key="kb.id" :value="kb.id">
              {{ kb.name }} ({{ kb.doc_count }})
            </option>
          </select>
          <button :disabled="syncing" @click="syncCurrentKB">
            {{ syncing ? '同步…' : '同步' }}
          </button>
        </div>

        <div v-if="activeKB" class="row" style="flex-wrap: wrap">
          <span class="badge">{{ activeKB.source_type === 'local_dir' ? '本地目录' : '远端 Git' }}</span>
          <span class="badge">{{ activeKB.readonly ? '只读' : '可编辑' }}</span>
          <span v-if="activeKB.last_commit_sha" class="badge">
            <code>{{ activeKB.last_commit_sha.slice(0, 8) }}</code>
          </span>
        </div>

        <div class="row" style="gap: 6px">
          <input
            v-model="treeFilter"
            placeholder="过滤目录或文档名…"
            style="flex: 1; min-width: 0; font-size: 12px; padding: 5px 8px"
          />
          <button class="ghost tree-tool-btn" title="全部展开" @click="expandAllDirs">展开</button>
          <button class="ghost tree-tool-btn" title="全部折叠" @click="collapseAllDirs">折叠</button>
        </div>

        <button
          v-if="canEditActiveKB"
          class="primary"
          @click="openNewDocInDir()"
        >
          {{ showNewDoc ? '取消新建' : '+ 新建文档' }}
        </button>

        <div class="doc-tree">
          <template v-for="row in visibleTreeRows" :key="row.key">
            <div
              v-if="row.kind === 'dir'"
              class="tree-row dir-row"
              :style="{ paddingLeft: `${row.depth * 14 + 8}px` }"
              @click="toggleDir(row.path)"
            >
              <span class="caret">{{ row.expanded ? '▾' : '▸' }}</span>
              <span class="dir-icon" aria-hidden="true">📁</span>
              <span class="tree-name" :title="row.path">{{ row.name }}</span>
              <span class="dir-count">{{ row.docCount }}</span>
              <button
                v-if="canEditActiveKB"
                class="ghost dir-add-btn"
                :title="`在 ${row.path}/ 下新建文档`"
                @click.stop="openNewDocInDir(row.path)"
              >
                +
              </button>
            </div>

            <div
              v-else
              class="tree-row file-row"
              :class="{ selected: activeDoc?.doc.rel_path === row.path && !showNewDoc && workspaceMode === 'doc' }"
              :style="{ paddingLeft: `${row.depth * 14 + 20}px` }"
              :title="row.path"
              @click="row.doc && openDoc(row.doc.kb_id, row.doc.rel_path)"
            >
              <span class="file-dot"></span>
              <span class="tree-name">{{ row.name }}</span>
            </div>
          </template>

          <p v-if="visibleTreeRows.length === 0" class="muted" style="text-align: center; padding: 16px 0">
            {{ docs.length === 0 ? '该知识库暂无 Markdown / 文本文档。' : '未匹配到符合过滤条件的目录或文档。' }}
          </p>
        </div>
      </aside>

      <!-- Right Pane: Per-KB Knowledge Graph -->
      <section v-if="workspaceMode === 'graph'" class="stack" style="min-width: 0">
        <KnowledgeGraphView
          :graph="kbGraph"
          :active-doc-path="activeDoc?.doc.rel_path"
          :loading="loadingGraph"
          @open-doc="openFromGraph"
          @refresh="loadGraph(selectedKBId)"
        />
      </section>

      <!-- Right Pane: Document Reader / Split Editor -->
      <section v-else class="card stack doc-main-col">
        <!-- New Doc Form -->
        <template v-if="showNewDoc && canEditActiveKB">
          <div class="row" style="justify-content: space-between">
            <strong>在「{{ activeKB?.name }}」中新建文档</strong>
            <button class="primary" :disabled="saving" @click="createNewDoc">
              {{
                saving
                  ? activeKB?.source_type === 'remote_git'
                    ? '提交并推送中…'
                    : '创建中…'
                  : activeKB?.source_type === 'remote_git'
                    ? '创建并推送到远端'
                    : '创建并索引'
              }}
            </button>
          </div>
          <input
            v-model="newDocForm.path"
            placeholder="相对路径（支持自动创建多级目录），例如 architecture/adr-001.md"
          />
          <textarea
            v-model="newDocForm.content"
            rows="20"
            class="md-editor"
            placeholder="输入 Markdown 正文…"
          ></textarea>
        </template>

        <!-- Active Doc Reader / Split / Edit -->
        <template v-else-if="activeDoc">
          <div class="row doc-header">
            <div class="stack" style="gap: 4px; min-width: 0; flex: 1">
              <h3 style="margin: 0; font-size: 17px">{{ activeDoc.doc.title }}</h3>
              <div class="row breadcrumb-row">
                <template v-for="(crumb, idx) in activeBreadcrumbs" :key="crumb.dirPath">
                  <span v-if="idx > 0" class="muted">/</span>
                  <button
                    v-if="!crumb.isFile"
                    class="ghost crumb-btn"
                    @click="expandAncestors(activeDoc.doc.rel_path)"
                  >
                    {{ crumb.label }}
                  </button>
                  <code v-else style="font-size: 12px">{{ crumb.label }}</code>
                </template>
                <span class="muted" style="font-size: 11px">· {{ activeDoc.doc.size_bytes }} bytes</span>
              </div>
            </div>

            <div class="row" style="flex-wrap: wrap; gap: 6px">
              <div class="mode-switch">
                <button
                  :class="{ active: docViewMode === 'read' }"
                  @click="docViewMode = 'read'"
                >
                  阅读
                </button>
                <button
                  v-if="canEditActiveKB"
                  :class="{ active: docViewMode === 'split' }"
                  @click="docViewMode = 'split'"
                >
                  分栏
                </button>
                <button
                  :class="{ active: docViewMode === 'edit' }"
                  @click="docViewMode = 'edit'"
                >
                  {{ canEditActiveKB ? '编辑' : '源码' }}
                </button>
              </div>

              <button
                v-if="docViewMode === 'read' && renderedDoc.toc.length"
                class="ghost"
                style="font-size: 12px"
                @click="showToc = !showToc"
              >
                {{ showToc ? '隐藏大纲' : '显示大纲' }}
              </button>

              <span v-if="!canEditActiveKB" class="badge">只读</span>
              <button
                v-else
                class="primary"
                :disabled="saving || !dirty"
                @click="saveCurrentDoc"
              >
                {{
                  saving
                    ? activeKB?.source_type === 'remote_git'
                      ? '变基并推送中…'
                      : '保存中…'
                    : dirty
                      ? activeKB?.source_type === 'remote_git'
                        ? '保存并推送'
                        : '保存'
                      : '已保存'
                }}
              </button>
            </div>
          </div>

          <!-- 1. Reading Mode (Markdown Render + Frontmatter Card + Right TOC + Backlinks) -->
          <div
            v-if="docViewMode === 'read'"
            class="reader-grid"
            :class="{ 'with-toc': showToc && renderedDoc.toc.length > 0 }"
          >
            <div ref="previewPaneRef" class="reader-body stack">
              <!-- Frontmatter Properties Card -->
              <div v-if="renderedDoc.frontmatter.length" class="fm-card">
                <button class="fm-head" @click="showFrontmatter = !showFrontmatter">
                  <span>{{ showFrontmatter ? '▾' : '▸' }} 文档属性 ({{ renderedDoc.frontmatter.length }})</span>
                </button>
                <div v-if="showFrontmatter" class="fm-grid">
                  <template v-for="field in renderedDoc.frontmatter" :key="field.key">
                    <div class="fm-key">{{ field.key }}</div>
                    <div class="fm-val">
                      <template v-if="Array.isArray(field.value)">
                        <span v-for="tag in field.value" :key="tag" class="fm-pill">{{ tag }}</span>
                        <span v-if="field.value.length === 0" class="muted">-</span>
                      </template>
                      <span v-else>{{ field.value || '-' }}</span>
                    </div>
                  </template>
                </div>
              </div>

              <!-- Rendered Markdown -->
              <article
                class="md-prose"
                v-html="renderedDoc.html"
                @click="onMarkdownClick"
              ></article>

              <!-- Backlinks Footer -->
              <div v-if="activeBacklinks.length" class="backlinks-card stack">
                <strong style="font-size: 13px">反向链接 ({{ activeBacklinks.length }})</strong>
                <div class="row" style="flex-wrap: wrap; gap: 6px">
                  <button
                    v-for="bl in activeBacklinks"
                    :key="bl.relPath"
                    class="backlink-chip"
                    :title="bl.relPath"
                    @click="activeKB && openDoc(activeKB.id, bl.relPath)"
                  >
                    ↖ {{ bl.title }}
                  </button>
                </div>
              </div>
            </div>

            <!-- Right Heading Outline (TOC) -->
            <aside v-if="showToc && renderedDoc.toc.length" class="toc-rail">
              <div class="toc-title">目录大纲</div>
              <button
                v-for="item in renderedDoc.toc"
                :key="item.id"
                class="toc-item"
                :style="{ paddingLeft: `${(item.level - 1) * 11 + 6}px` }"
                @click="scrollToHeading(item.id)"
              >
                {{ item.text }}
              </button>
            </aside>
          </div>

          <!-- 2. Split Mode (Live Editor + Live Rendered Preview) -->
          <div v-else-if="docViewMode === 'split' && canEditActiveKB" class="split-grid">
            <textarea
              v-model="editorContent"
              class="md-editor split-editor"
              @input="dirty = true"
            ></textarea>
            <div ref="previewPaneRef" class="split-preview stack">
              <div v-if="renderedDoc.frontmatter.length" class="fm-card">
                <div class="fm-grid">
                  <template v-for="field in renderedDoc.frontmatter" :key="field.key">
                    <div class="fm-key">{{ field.key }}</div>
                    <div class="fm-val">
                      <template v-if="Array.isArray(field.value)">
                        <span v-for="tag in field.value" :key="tag" class="fm-pill">{{ tag }}</span>
                      </template>
                      <span v-else>{{ field.value || '-' }}</span>
                    </div>
                  </template>
                </div>
              </div>
              <article
                class="md-prose"
                v-html="renderedDoc.html"
                @click="onMarkdownClick"
              ></article>
            </div>
          </div>

          <!-- 3. Edit / Raw Source Mode -->
          <template v-else>
            <textarea
              v-if="canEditActiveKB"
              v-model="editorContent"
              rows="26"
              class="md-editor"
              @input="dirty = true"
            ></textarea>
            <pre v-else class="md-readonly">{{ activeDoc.content }}</pre>
          </template>
        </template>

        <div v-else class="muted" style="text-align: center; padding: 48px 0">
          从左侧目录树选择文档浏览，或在上方执行全文检索。
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.kb-layout {
  display: grid;
  grid-template-columns: 320px 1fr;
  gap: 14px;
  align-items: start;
}
.sidebar-col {
  gap: 8px;
}
.doc-main-col {
  min-width: 0;
}
.doc-header {
  justify-content: space-between;
  align-items: flex-start;
  flex-wrap: wrap;
  gap: 10px;
  border-bottom: 1px solid var(--border-subtle);
  padding-bottom: 10px;
}
.mode-switch {
  display: inline-flex;
  border: 1px solid var(--border-subtle);
  border-radius: 7px;
  overflow: hidden;
  background: rgba(7, 11, 20, 0.55);
}
.mode-switch button {
  border: none;
  border-radius: 0;
  padding: 4px 10px;
  font-size: 12px;
  background: transparent;
}
.mode-switch button.active {
  background: rgba(56, 189, 248, 0.22);
  color: #f8fafc;
}
.search-panel {
  border-top: 1px solid var(--border-subtle);
  padding-top: 10px;
}
.hit-row {
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 9px 11px;
  cursor: pointer;
  background: rgba(7, 11, 20, 0.45);
}
.hit-row:hover {
  border-color: var(--accent-cyan);
  background: rgba(56, 189, 248, 0.08);
}
.tree-tool-btn {
  padding: 4px 7px;
  font-size: 11px;
  white-space: nowrap;
}
.doc-tree {
  max-height: 620px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
  border-top: 1px solid var(--border-subtle);
  padding-top: 6px;
}
.tree-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  border-radius: 6px;
  cursor: pointer;
  user-select: none;
  font-size: 13px;
  line-height: 1.35;
}
.dir-row {
  color: #e2e8f0;
  font-weight: 500;
}
.dir-row:hover {
  background: rgba(148, 163, 184, 0.1);
}
.caret {
  width: 12px;
  font-size: 11px;
  color: var(--text-secondary);
  flex-shrink: 0;
  text-align: center;
}
.dir-icon {
  font-size: 12px;
  flex-shrink: 0;
  opacity: 0.85;
}
.dir-count {
  font-size: 11px;
  color: var(--text-secondary);
  margin-left: auto;
  padding: 0 5px;
  border-radius: 999px;
  background: rgba(148, 163, 184, 0.12);
  flex-shrink: 0;
}
.dir-add-btn {
  padding: 0 5px;
  font-size: 13px;
  line-height: 1.2;
  opacity: 0.45;
  flex-shrink: 0;
}
.dir-row:hover .dir-add-btn {
  opacity: 1;
  color: var(--accent-cyan);
}
.file-row {
  color: #cbd5e1;
  border: 1px solid transparent;
}
.file-row:hover {
  background: rgba(56, 189, 248, 0.08);
}
.file-row.selected {
  border-color: var(--accent-cyan);
  background: rgba(56, 189, 248, 0.14);
  color: #f8fafc;
}
.file-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--text-secondary);
  flex-shrink: 0;
}
.file-row.selected .file-dot {
  background: var(--accent-cyan);
}
.tree-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}
.breadcrumb-row {
  flex-wrap: wrap;
  gap: 4px;
  font-size: 12px;
}
.crumb-btn {
  padding: 1px 4px;
  font-size: 12px;
  color: var(--accent-cyan);
}

/* Reader + TOC Layout */
.reader-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 16px;
  align-items: start;
}
.reader-grid.with-toc {
  grid-template-columns: minmax(0, 1fr) 210px;
}
.reader-body {
  min-width: 0;
  max-height: 680px;
  overflow-y: auto;
  padding-right: 6px;
}
.toc-rail {
  border-left: 1px solid var(--border-subtle);
  padding-left: 10px;
  max-height: 660px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.toc-title {
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--text-secondary);
  margin-bottom: 4px;
}
.toc-item {
  text-align: left;
  background: transparent;
  border: none;
  padding: 3px 6px;
  font-size: 12px;
  color: #94a3b8;
  border-radius: 5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.toc-item:hover {
  background: rgba(56, 189, 248, 0.1);
  color: #f8fafc;
}

/* Frontmatter Properties Card */
.fm-card {
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  background: rgba(7, 11, 20, 0.55);
  padding: 8px 12px;
}
.fm-head {
  background: transparent;
  border: none;
  padding: 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  cursor: pointer;
}
.fm-grid {
  display: grid;
  grid-template-columns: 120px 1fr;
  gap: 6px 12px;
  margin-top: 8px;
  font-size: 12px;
}
.fm-key {
  color: var(--text-secondary);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.fm-val {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
  color: #e2e8f0;
  word-break: break-all;
}
.fm-pill {
  font-size: 11px;
  padding: 1px 7px;
  border-radius: 999px;
  background: rgba(56, 189, 248, 0.14);
  border: 1px solid rgba(56, 189, 248, 0.3);
  color: #7dd3fc;
}

/* Backlinks Card */
.backlinks-card {
  border-top: 1px solid var(--border-subtle);
  padding-top: 12px;
  margin-top: 12px;
}
.backlink-chip {
  font-size: 12px;
  padding: 4px 10px;
  border-radius: 6px;
  background: rgba(56, 189, 248, 0.08);
  border: 1px solid rgba(56, 189, 248, 0.25);
  color: var(--accent-cyan);
}
.backlink-chip:hover {
  background: rgba(56, 189, 248, 0.18);
}

/* Split Editor + Live Preview */
.split-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  height: 640px;
}
.split-editor {
  height: 100%;
  resize: none;
}
.split-preview {
  height: 100%;
  overflow-y: auto;
  padding: 12px 14px;
  border-radius: 8px;
  border: 1px solid var(--border-subtle);
  background: rgba(7, 11, 20, 0.42);
}

/* Rendered Markdown Typography (Obsidian-inspired dark prose) */
.md-prose {
  color: #dbe4f0;
  font-size: 14px;
  line-height: 1.72;
  word-break: break-word;
}
.md-prose :deep(h1),
.md-prose :deep(h2),
.md-prose :deep(h3),
.md-prose :deep(h4) {
  color: #f8fafc;
  margin: 1.2em 0 0.55em;
  line-height: 1.35;
  scroll-margin-top: 12px;
}
.md-prose :deep(h1) {
  font-size: 1.45em;
  border-bottom: 1px solid var(--border-subtle);
  padding-bottom: 6px;
}
.md-prose :deep(h2) {
  font-size: 1.22em;
  border-bottom: 1px solid rgba(148, 163, 184, 0.14);
  padding-bottom: 4px;
}
.md-prose :deep(h3) {
  font-size: 1.08em;
}
.md-prose :deep(p) {
  margin: 0.65em 0;
}
.md-prose :deep(ul),
.md-prose :deep(ol) {
  padding-left: 1.5em;
  margin: 0.5em 0;
}
.md-prose :deep(li) {
  margin: 0.25em 0;
}
.md-prose :deep(blockquote) {
  margin: 0.8em 0;
  padding: 6px 12px;
  border-left: 3px solid var(--accent-cyan);
  background: rgba(56, 189, 248, 0.06);
  color: #cbd5e1;
  border-radius: 0 6px 6px 0;
}
.md-prose :deep(code) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.88em;
  padding: 1px 5px;
  border-radius: 4px;
  background: rgba(148, 163, 184, 0.14);
  color: #e2e8f0;
}
.md-prose :deep(pre) {
  background: #060a12;
  border: 1px solid var(--border-subtle);
  border-radius: 8px;
  padding: 12px 14px;
  overflow-x: auto;
  margin: 0.8em 0;
}
.md-prose :deep(pre code) {
  background: transparent;
  padding: 0;
  font-size: 12.5px;
  line-height: 1.55;
}
.md-prose :deep(table) {
  width: 100%;
  border-collapse: collapse;
  margin: 0.9em 0;
  font-size: 13px;
}
.md-prose :deep(th),
.md-prose :deep(td) {
  border: 1px solid var(--border-subtle);
  padding: 6px 10px;
  text-align: left;
}
.md-prose :deep(th) {
  background: rgba(30, 41, 59, 0.65);
  color: #f8fafc;
  font-weight: 600;
}
.md-prose :deep(hr) {
  border: none;
  border-top: 1px solid var(--border-subtle);
  margin: 1.2em 0;
}
.md-prose :deep(a) {
  color: var(--accent-cyan);
  text-decoration: none;
}
.md-prose :deep(a:hover) {
  text-decoration: underline;
}
.md-prose :deep(a.wiki-link) {
  color: #7dd3fc;
  background: rgba(56, 189, 248, 0.1);
  padding: 0 4px;
  border-radius: 4px;
  border-bottom: 1px dashed rgba(56, 189, 248, 0.55);
}
.md-prose :deep(a.wiki-link:hover) {
  background: rgba(56, 189, 248, 0.2);
  text-decoration: none;
}
.md-prose :deep(a.wiki-link.unresolved) {
  color: #94a3b8;
  background: rgba(148, 163, 184, 0.1);
  border-bottom-color: rgba(148, 163, 184, 0.4);
}
.md-editor {
  width: 100%;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 13px;
  line-height: 1.55;
  resize: vertical;
}
.md-readonly {
  margin: 0;
  padding: 14px;
  border-radius: 8px;
  border: 1px solid var(--border-subtle);
  background: rgba(7, 11, 20, 0.55);
  white-space: pre-wrap;
  word-break: break-word;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 13px;
  line-height: 1.6;
  max-height: 640px;
  overflow-y: auto;
}
@media (max-width: 1080px) {
  .kb-layout,
  .reader-grid.with-toc,
  .split-grid {
    grid-template-columns: 1fr;
  }
  .split-grid {
    height: auto;
  }
  .split-editor,
  .split-preview {
    height: 420px;
  }
}
</style>
