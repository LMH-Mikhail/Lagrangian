<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { KnowledgeGraph, KnowledgeGraphNode } from '../types/models'

const props = defineProps<{
  graph: KnowledgeGraph | null
  activeDocPath?: string
  loading?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-doc', relPath: string): void
  (e: 'refresh'): void
}>()

interface SimNode extends KnowledgeGraphNode {
  x: number
  y: number
  vx: number
  vy: number
  radius: number
  color: string
  pinned?: boolean
}

interface SimEdge {
  sourceIdx: number
  targetIdx: number
  sameGroup: boolean
}

const palette = [
  '#38bdf8', // sky
  '#34d399', // emerald
  '#a78bfa', // violet
  '#f59e0b', // amber
  '#fb7185', // rose
  '#22d3ee', // cyan
  '#f472b6', // pink
  '#4ade80', // green
  '#60a5fa', // blue
  '#facc15', // yellow
  '#c084fc', // purple
  '#fb923c', // orange
]

const canvasWrap = ref<HTMLElement | null>(null)
const canvasEl = ref<HTMLCanvasElement | null>(null)

const showControls = ref(false)
const openPanels = reactive({
  filter: true,
  groups: true,
  display: false,
  forces: false,
})

// User controls matching Obsidian Graph View
const filterQuery = ref('')
const showOrphans = ref(true)
const activeGroupFilter = ref('')
const colorByGroup = ref(true)
const nodeSizeScale = ref(1.0)
const edgeOpacity = ref(0.18)
const showAllLabels = ref(false)

const forces = reactive({
  center: 0.012,
  repel: 950,
  linkDist: 58,
  linkStrength: 0.032,
})

const hoveredNode = ref<SimNode | null>(null)
const tooltipPos = reactive({ x: 0, y: 0 })
const visibleNodeCount = ref(0)
const visibleEdgeCount = ref(0)

// Internal simulation & camera state (non-reactive for 60fps Canvas loop)
let simNodes: SimNode[] = []
let simEdges: SimEdge[] = []
let neighborMap: Map<number, Set<number>> = new Map()
let nodeIndexById: Map<string, number> = new Map()
let rafId = 0
let alpha = 0
let width = 800
let height = 620
let dpr = 1
const cam = { x: 0, y: 0, scale: 1 }

// Pointer interaction state
let pointerDown = false
let draggedIdx = -1
let hoveredIdx = -1
let lastX = 0
let lastY = 0
let movedDist = 0
let resizeObs: ResizeObserver | null = null

const groupColors = computed(() => {
  const m = new Map<string, string>()
  const groups = props.graph?.groups || []
  groups.forEach((g, idx) => {
    m.set(g, palette[idx % palette.length])
  })
  return m
})

const groupStats = computed(() => {
  const counts = new Map<string, number>()
  for (const n of props.graph?.nodes || []) {
    counts.set(n.group, (counts.get(n.group) || 0) + 1)
  }
  return (props.graph?.groups || []).map((g) => ({
    name: g,
    count: counts.get(g) || 0,
    color: groupColors.value.get(g) || '#94a3b8',
  }))
})

const visibleStats = computed(() => ({
  nodes: visibleNodeCount.value,
  edges: visibleEdgeCount.value,
  totalNodes: props.graph?.nodes.length || 0,
  totalEdges: props.graph?.edges.length || 0,
}))

function rebuildSimulation() {
  const g = props.graph
  if (!g) {
    simNodes = []
    simEdges = []
    visibleNodeCount.value = 0
    visibleEdgeCount.value = 0
    neighborMap = new Map()
    nodeIndexById = new Map()
    drawFrame()
    return
  }

  const q = filterQuery.value.trim().toLowerCase()
  const grpFilter = activeGroupFilter.value

  const kept = g.nodes.filter((n) => {
    if (!showOrphans.value && n.degree === 0) return false
    if (grpFilter && n.group !== grpFilter) return false
    if (q && !n.title.toLowerCase().includes(q) && !n.id.toLowerCase().includes(q)) return false
    return true
  })

  // Seed deterministic group-clustered initial positions on well-separated islands
  const groupIdxMap = new Map<string, number>()
  g.groups.forEach((grp, i) => groupIdxMap.set(grp, i))
  const totalGroups = Math.max(g.groups.length, 1)
  const spread = Math.max(240, Math.sqrt(kept.length) * 26)

  nodeIndexById = new Map()
  simNodes = kept.map((n, idx) => {
    nodeIndexById.set(n.id, idx)
    const gi = groupIdxMap.get(n.group) ?? 0
    const clusterAngle = (2 * Math.PI * gi) / totalGroups
    const localAngle = idx * 2.399963229728653 // golden angle spiral inside cluster
    const isOrphan = n.degree === 0
    let x = 0
    let y = 0
    if (isOrphan) {
      const ringR = spread * 1.48 + (idx % 19) * 5
      x = Math.cos(localAngle) * ringR
      y = Math.sin(localAngle) * ringR
    } else {
      const cx = Math.cos(clusterAngle) * spread * 0.78
      const cy = Math.sin(clusterAngle) * spread * 0.78
      const localR = 14 + Math.sqrt((idx % 97) + 1) * 14
      x = cx + Math.cos(localAngle) * localR
      y = cy + Math.sin(localAngle) * localR
    }
    // Delicate Obsidian-like dot sizing: 2.2px for regular docs, up to 8.5px for major hubs
    const r = Math.min(8.5, 2.1 + Math.sqrt(n.degree) * 0.9)
    return {
      ...n,
      x,
      y,
      vx: 0,
      vy: 0,
      radius: r,
      color: groupColors.value.get(n.group) || '#94a3b8',
    }
  })

  neighborMap = new Map()
  simEdges = []
  for (const e of g.edges) {
    const si = nodeIndexById.get(e.source)
    const ti = nodeIndexById.get(e.target)
    if (si === undefined || ti === undefined || si === ti) continue
    simEdges.push({
      sourceIdx: si,
      targetIdx: ti,
      sameGroup: simNodes[si].group === simNodes[ti].group,
    })
    if (!neighborMap.has(si)) neighborMap.set(si, new Set())
    if (!neighborMap.has(ti)) neighborMap.set(ti, new Set())
    neighborMap.get(si)!.add(ti)
    neighborMap.get(ti)!.add(si)
  }

  visibleNodeCount.value = simNodes.length
  visibleEdgeCount.value = simEdges.length
  hoveredIdx = -1
  hoveredNode.value = null
  draggedIdx = -1

  // Pre-warm physics synchronously so clusters immediately settle cleanly without chaos
  alpha = 1.0
  const warmSteps = simNodes.length > 250 ? 90 : 120
  for (let s = 0; s < warmSteps; s++) {
    stepPhysics()
  }

  fitToView(false)
  reheat(0.28)
}

function reheat(initialAlpha = 0.65) {
  alpha = Math.max(alpha, initialAlpha)
  if (!rafId) {
    rafId = requestAnimationFrame(tick)
  }
}

function fitToView(redraw = true) {
  if (simNodes.length === 0) {
    cam.x = 0
    cam.y = 0
    cam.scale = 1
    if (redraw) drawFrame()
    return
  }
  let minX = Infinity
  let maxX = -Infinity
  let minY = Infinity
  let maxY = -Infinity
  for (const n of simNodes) {
    if (n.x < minX) minX = n.x
    if (n.x > maxX) maxX = n.x
    if (n.y < minY) minY = n.y
    if (n.y > maxY) maxY = n.y
  }
  const spanX = Math.max(120, maxX - minX)
  const spanY = Math.max(120, maxY - minY)
  const pad = 64
  const scaleX = (width - pad * 2) / spanX
  const scaleY = (height - pad * 2) / spanY
  cam.scale = Math.max(0.12, Math.min(1.6, Math.min(scaleX, scaleY)))
  cam.x = -(minX + maxX) / 2
  cam.y = -(minY + maxY) / 2
  if (redraw) drawFrame()
}

function stepPhysics() {
  const n = simNodes.length
  if (n === 0) return

  const repel = forces.repel
  const centerK = forces.center
  const linkDist = forces.linkDist
  const linkK = forces.linkStrength

  // 1. Pairwise repulsion (stronger separation so dense graphs stay airy and readable)
  for (let i = 0; i < n; i++) {
    const a = simNodes[i]
    for (let j = i + 1; j < n; j++) {
      const b = simNodes[j]
      const dx = b.x - a.x
      const dy = b.y - a.y
      const distSq = dx * dx + dy * dy + 25
      if (distSq > 900000) continue
      const f = (repel * alpha) / distSq
      const dist = Math.sqrt(distSq)
      const fx = (dx / dist) * f
      const fy = (dy / dist) * f
      a.vx -= fx
      a.vy -= fy
      b.vx += fx
      b.vy += fy
    }
  }

  // 2. Spring attraction along edges (cross-cluster links are looser so clusters stay distinct)
  for (let i = 0; i < simEdges.length; i++) {
    const e = simEdges[i]
    const a = simNodes[e.sourceIdx]
    const b = simNodes[e.targetIdx]
    const dx = b.x - a.x
    const dy = b.y - a.y
    const dist = Math.sqrt(dx * dx + dy * dy) || 0.001
    const desired = e.sameGroup ? linkDist : linkDist * 2.2
    const k = e.sameGroup ? linkK : linkK * 0.32
    const f = (dist - desired) * k * alpha
    const fx = (dx / dist) * f
    const fy = (dy / dist) * f
    a.vx += fx
    a.vy += fy
    b.vx -= fx
    b.vy -= fy
  }

  // 3. Gentle centering gravity + velocity damping
  const damping = 0.8
  for (let i = 0; i < n; i++) {
    const node = simNodes[i]
    if (node.pinned) {
      node.vx = 0
      node.vy = 0
      continue
    }
    const grav = node.degree === 0 ? centerK * 0.55 : centerK
    node.vx += -node.x * grav * alpha
    node.vy += -node.y * grav * alpha

    node.vx *= damping
    node.vy *= damping

    const speed = Math.hypot(node.vx, node.vy)
    if (speed > 20) {
      node.vx = (node.vx / speed) * 20
      node.vy = (node.vy / speed) * 20
    }
    node.x += node.vx
    node.y += node.vy
  }

  alpha *= 0.982
}

function worldToScreen(wx: number, wy: number) {
  return {
    sx: width / 2 + (wx + cam.x) * cam.scale,
    sy: height / 2 + (wy + cam.y) * cam.scale,
  }
}

function screenToWorld(sx: number, sy: number) {
  return {
    wx: (sx - width / 2) / cam.scale - cam.x,
    wy: (sy - height / 2) / cam.scale - cam.y,
  }
}

function drawFrame() {
  const canvas = canvasEl.value
  if (!canvas) return
  const ctx = canvas.getContext('2d')
  if (!ctx) return

  ctx.save()
  ctx.scale(dpr, dpr)
  ctx.clearRect(0, 0, width, height)

  const bg = ctx.createRadialGradient(
    width / 2,
    height / 2,
    24,
    width / 2,
    height / 2,
    Math.max(width, height) * 0.65,
  )
  bg.addColorStop(0, 'rgba(30, 41, 59, 0.28)')
  bg.addColorStop(1, 'rgba(7, 11, 20, 0.08)')
  ctx.fillStyle = bg
  ctx.fillRect(0, 0, width, height)

  ctx.translate(width / 2, height / 2)
  ctx.scale(cam.scale, cam.scale)
  ctx.translate(cam.x, cam.y)

  const focusIdx = hoveredIdx >= 0 ? hoveredIdx : -1
  const focusNeighbors = focusIdx >= 0 ? neighborMap.get(focusIdx) || new Set<number>() : null

  // 1. Draw delicate edges
  const baseLineW = Math.min(1.4, Math.max(0.45, 0.8 / cam.scale))
  for (let i = 0; i < simEdges.length; i++) {
    const e = simEdges[i]
    const a = simNodes[e.sourceIdx]
    const b = simNodes[e.targetIdx]
    const isHighlight = focusIdx >= 0 && (e.sourceIdx === focusIdx || e.targetIdx === focusIdx)
    if (focusIdx >= 0) {
      if (!isHighlight) {
        ctx.strokeStyle = `rgba(148, 163, 184, ${Math.min(0.04, edgeOpacity.value * 0.2)})`
        ctx.lineWidth = baseLineW * 0.7
      } else {
        ctx.strokeStyle = 'rgba(56, 189, 248, 0.85)'
        ctx.lineWidth = Math.min(2.2, 1.4 / cam.scale)
      }
    } else {
      ctx.strokeStyle = `rgba(148, 163, 184, ${edgeOpacity.value})`
      ctx.lineWidth = baseLineW
    }
    ctx.beginPath()
    ctx.moveTo(a.x, a.y)
    ctx.lineTo(b.x, b.y)
    ctx.stroke()
  }

  // 2. Draw crisp Obsidian-proportioned nodes
  const sizeScale = nodeSizeScale.value
  const activePath = props.activeDocPath || ''
  for (let i = 0; i < simNodes.length; i++) {
    const node = simNodes[i]
    const isFocus = i === focusIdx
    const isNeighbor = focusNeighbors ? focusNeighbors.has(i) : false
    const isActiveDoc = activePath !== '' && node.id === activePath
    const dimmed = focusIdx >= 0 && !isFocus && !isNeighbor

    const r = node.radius * sizeScale
    const baseColor = colorByGroup.value ? node.color : '#94a3b8'

    ctx.globalAlpha = dimmed ? 0.14 : 0.94
    if (isFocus || isActiveDoc) {
      ctx.beginPath()
      ctx.arc(node.x, node.y, r + 4 / cam.scale, 0, Math.PI * 2)
      ctx.fillStyle = isFocus ? 'rgba(56, 189, 248, 0.28)' : 'rgba(245, 158, 11, 0.3)'
      ctx.fill()
    }

    ctx.beginPath()
    ctx.arc(node.x, node.y, r, 0, Math.PI * 2)
    ctx.fillStyle = isFocus ? '#38bdf8' : isActiveDoc ? '#f59e0b' : baseColor
    ctx.fill()

    if (isActiveDoc || isFocus) {
      ctx.strokeStyle = '#f8fafc'
      ctx.lineWidth = 1.3 / cam.scale
      ctx.stroke()
    }
  }

  // 3. Draw labels cleanly: never flood overview; show on hover+neighbors, zoomed in (>= 1.45), or explicit toggle
  ctx.globalAlpha = 1
  const screenFontPx = 11
  const worldFontPx = screenFontPx / cam.scale
  ctx.font = `${worldFontPx}px -apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif`
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'

  let neighborLabelsDrawn = 0
  for (let i = 0; i < simNodes.length; i++) {
    const node = simNodes[i]
    const isFocus = i === focusIdx
    const isNeighbor = focusNeighbors ? focusNeighbors.has(i) : false
    const isActiveDoc = activePath !== '' && node.id === activePath

    let shouldShow = false
    if (isFocus || isActiveDoc) {
      shouldShow = true
    } else if (isNeighbor && neighborLabelsDrawn < 14) {
      shouldShow = true
      neighborLabelsDrawn++
    } else if (showAllLabels.value && focusIdx < 0) {
      shouldShow = true
    } else if (focusIdx < 0 && cam.scale >= 1.55) {
      shouldShow = true
    }

    if (!shouldShow) continue
    const r = node.radius * sizeScale
    ctx.fillStyle = isFocus || isActiveDoc ? '#f8fafc' : 'rgba(226, 232, 240, 0.86)'
    const label = node.title.length > 20 ? node.title.slice(0, 19) + '…' : node.title
    ctx.fillText(label, node.x, node.y + r + 3 / cam.scale)
  }

  ctx.restore()
}

function tick() {
  rafId = 0
  if (alpha > 0.008 || draggedIdx >= 0) {
    stepPhysics()
    drawFrame()
    rafId = requestAnimationFrame(tick)
  } else {
    drawFrame()
  }
}

function pickNodeAt(sx: number, sy: number): number {
  const { wx, wy } = screenToWorld(sx, sy)
  const hitPad = 6 / cam.scale
  for (let i = simNodes.length - 1; i >= 0; i--) {
    const node = simNodes[i]
    const r = node.radius * nodeSizeScale.value + hitPad
    const dx = wx - node.x
    const dy = wy - node.y
    if (dx * dx + dy * dy <= r * r) {
      return i
    }
  }
  return -1
}

function getLocalCoords(ev: MouseEvent) {
  const rect = canvasEl.value!.getBoundingClientRect()
  return { sx: ev.clientX - rect.left, sy: ev.clientY - rect.top }
}

function onMouseDown(ev: MouseEvent) {
  if (!canvasEl.value) return
  const { sx, sy } = getLocalCoords(ev)
  pointerDown = true
  lastX = sx
  lastY = sy
  movedDist = 0
  draggedIdx = pickNodeAt(sx, sy)
  if (draggedIdx >= 0) {
    simNodes[draggedIdx].pinned = true
    reheat(0.35)
  }
}

function onMouseMove(ev: MouseEvent) {
  if (!canvasEl.value) return
  const { sx, sy } = getLocalCoords(ev)

  if (pointerDown) {
    const dx = sx - lastX
    const dy = sy - lastY
    movedDist += Math.hypot(dx, dy)
    lastX = sx
    lastY = sy

    if (draggedIdx >= 0) {
      const { wx, wy } = screenToWorld(sx, sy)
      simNodes[draggedIdx].x = wx
      simNodes[draggedIdx].y = wy
      reheat(0.22)
    } else {
      cam.x += dx / cam.scale
      cam.y += dy / cam.scale
      drawFrame()
    }
    return
  }

  const hit = pickNodeAt(sx, sy)
  if (hit !== hoveredIdx) {
    hoveredIdx = hit
    hoveredNode.value = hit >= 0 ? simNodes[hit] : null
    drawFrame()
  }
  if (hit >= 0) {
    const pos = worldToScreen(simNodes[hit].x, simNodes[hit].y)
    tooltipPos.x = Math.min(width - 250, Math.max(12, pos.sx + 14))
    tooltipPos.y = Math.min(height - 96, Math.max(12, pos.sy - 14))
  }
}

function onMouseUp() {
  if (!pointerDown) return
  pointerDown = false
  if (draggedIdx >= 0) {
    const idx = draggedIdx
    simNodes[idx].pinned = false
    draggedIdx = -1
    if (movedDist < 5) {
      emit('open-doc', simNodes[idx].id)
    }
  }
}

function onMouseLeave() {
  if (pointerDown && draggedIdx >= 0) {
    simNodes[draggedIdx].pinned = false
  }
  pointerDown = false
  draggedIdx = -1
  if (hoveredIdx >= 0) {
    hoveredIdx = -1
    hoveredNode.value = null
    drawFrame()
  }
}

function onWheel(ev: WheelEvent) {
  if (!canvasEl.value) return
  ev.preventDefault()
  const { sx, sy } = getLocalCoords(ev)
  const before = screenToWorld(sx, sy)
  const factor = ev.deltaY < 0 ? 1.14 : 0.88
  cam.scale = Math.max(0.1, Math.min(4.5, cam.scale * factor))
  const after = screenToWorld(sx, sy)
  cam.x += after.wx - before.wx
  cam.y += after.wy - before.wy
  drawFrame()
}

function syncCanvasSize() {
  if (!canvasWrap.value || !canvasEl.value) return
  const rect = canvasWrap.value.getBoundingClientRect()
  width = Math.max(320, Math.floor(rect.width))
  height = Math.max(420, Math.floor(rect.height))
  dpr = Math.min(window.devicePixelRatio || 1, 2)
  canvasEl.value.width = Math.floor(width * dpr)
  canvasEl.value.height = Math.floor(height * dpr)
  canvasEl.value.style.width = `${width}px`
  canvasEl.value.style.height = `${height}px`
  drawFrame()
}

function toggleGroupFilter(grp: string) {
  activeGroupFilter.value = activeGroupFilter.value === grp ? '' : grp
}

onMounted(() => {
  syncCanvasSize()
  if (canvasWrap.value) {
    resizeObs = new ResizeObserver(syncCanvasSize)
    resizeObs.observe(canvasWrap.value)
  }
  rebuildSimulation()
})

onBeforeUnmount(() => {
  if (rafId) cancelAnimationFrame(rafId)
  if (resizeObs) resizeObs.disconnect()
})

watch(() => props.graph, rebuildSimulation)
watch([filterQuery, showOrphans, activeGroupFilter], rebuildSimulation)
watch([colorByGroup, nodeSizeScale, edgeOpacity, showAllLabels, () => props.activeDocPath], drawFrame)
watch(
  () => [forces.center, forces.repel, forces.linkDist, forces.linkStrength],
  () => reheat(0.75),
)
</script>

<template>
  <div class="graph-shell">
    <div ref="canvasWrap" class="graph-canvas-wrap">
      <canvas
        ref="canvasEl"
        class="graph-canvas"
        @mousedown="onMouseDown"
        @mousemove="onMouseMove"
        @mouseup="onMouseUp"
        @mouseleave="onMouseLeave"
        @wheel.prevent="onWheel"
      ></canvas>

      <!-- Loading / empty overlay -->
      <div v-if="loading" class="graph-overlay muted">正在解析知识库双链关系图谱…</div>
      <div v-else-if="!graph || graph.nodes.length === 0" class="graph-overlay muted">
        当前知识库暂无可展示的文档节点。
      </div>

      <!-- Hover tooltip -->
      <div
        v-if="hoveredNode"
        class="graph-tooltip"
        :style="{ left: `${tooltipPos.x}px`, top: `${tooltipPos.y}px` }"
      >
        <div class="tooltip-title">{{ hoveredNode.title }}</div>
        <div class="muted" style="font-size: 11px">{{ hoveredNode.id }}</div>
        <div class="row" style="margin-top: 4px; gap: 6px">
          <span class="badge">{{ hoveredNode.group }}</span>
          <span class="muted" style="font-size: 11px">
            入链 {{ hoveredNode.in_links }} · 出链 {{ hoveredNode.out_links }}
          </span>
        </div>
      </div>

      <!-- Top-left stats & quick actions -->
      <div class="graph-hud-tl row">
        <span class="badge">
          {{ graph?.kb_name || '知识图谱' }} · 节点 {{ visibleStats.nodes }}/{{ visibleStats.totalNodes }} · 关联 {{ visibleStats.edges }}
        </span>
        <button class="hud-btn" @click="fitToView(true)">自适应全景</button>
        <button class="hud-btn" @click="reheat(0.95)">重新布局</button>
        <button class="hud-btn" @click="emit('refresh')">刷新图谱</button>
      </div>

      <!-- Bottom-left interaction hint -->
      <div class="graph-hud-bl muted">
        滚轮放大显示文档标题 · 悬停高亮关联邻域 · 点击节点打开文档
      </div>

      <!-- Top-right floating Obsidian-style Control Panel -->
      <div class="graph-panel-wrap">
        <button class="hud-btn" style="align-self: flex-end" @click="showControls = !showControls">
          {{ showControls ? '收起筛选面板 ×' : '筛选与设置 ⚙' }}
        </button>

        <div v-if="showControls" class="graph-panel stack">
          <!-- 1. 筛选 -->
          <div class="panel-sec">
            <button class="sec-head" @click="openPanels.filter = !openPanels.filter">
              <span>{{ openPanels.filter ? '▾' : '▸' }} 筛选</span>
            </button>
            <div v-if="openPanels.filter" class="sec-body stack">
              <input
                v-model="filterQuery"
                placeholder="搜索文档标题或路径…"
                style="font-size: 12px; padding: 5px 8px"
              />
              <label class="row" style="cursor: pointer; font-size: 12px">
                <input v-model="showOrphans" type="checkbox" style="width: auto" />
                <span>显示无双链孤立文档</span>
              </label>
            </div>
          </div>

          <!-- 2. 颜色组 -->
          <div class="panel-sec">
            <button class="sec-head" @click="openPanels.groups = !openPanels.groups">
              <span>{{ openPanels.groups ? '▾' : '▸' }} 颜色组 ({{ groupStats.length }})</span>
            </button>
            <div v-if="openPanels.groups" class="sec-body stack">
              <label class="row" style="cursor: pointer; font-size: 12px">
                <input v-model="colorByGroup" type="checkbox" style="width: auto" />
                <span>按顶层目录自动着色</span>
              </label>
              <div class="group-list">
                <button
                  v-for="g in groupStats"
                  :key="g.name"
                  class="group-chip"
                  :class="{ active: activeGroupFilter === g.name }"
                  @click="toggleGroupFilter(g.name)"
                >
                  <span class="dot" :style="{ background: g.color }"></span>
                  <span class="group-name">{{ g.name }}</span>
                  <span class="muted">{{ g.count }}</span>
                </button>
              </div>
            </div>
          </div>

          <!-- 3. 外观 -->
          <div class="panel-sec">
            <button class="sec-head" @click="openPanels.display = !openPanels.display">
              <span>{{ openPanels.display ? '▾' : '▸' }} 外观</span>
            </button>
            <div v-if="openPanels.display" class="sec-body stack">
              <label class="slider-row">
                <span>节点大小 ({{ nodeSizeScale.toFixed(1) }}x)</span>
                <input v-model.number="nodeSizeScale" type="range" min="0.5" max="2.0" step="0.1" />
              </label>
              <label class="slider-row">
                <span>连线透明度 ({{ Math.round(edgeOpacity * 100) }}%)</span>
                <input v-model.number="edgeOpacity" type="range" min="0.06" max="0.75" step="0.04" />
              </label>
              <label class="row" style="cursor: pointer; font-size: 12px">
                <input v-model="showAllLabels" type="checkbox" style="width: auto" />
                <span>始终显示所有节点标题</span>
              </label>
            </div>
          </div>

          <!-- 4. 力度 -->
          <div class="panel-sec">
            <button class="sec-head" @click="openPanels.forces = !openPanels.forces">
              <span>{{ openPanels.forces ? '▾' : '▸' }} 力度</span>
            </button>
            <div v-if="openPanels.forces" class="sec-body stack">
              <label class="slider-row">
                <span>中心引力</span>
                <input v-model.number="forces.center" type="range" min="0.004" max="0.05" step="0.002" />
              </label>
              <label class="slider-row">
                <span>节点排斥力</span>
                <input v-model.number="forces.repel" type="range" min="200" max="2200" step="50" />
              </label>
              <label class="slider-row">
                <span>连线距离</span>
                <input v-model.number="forces.linkDist" type="range" min="24" max="140" step="4" />
              </label>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.graph-shell {
  width: 100%;
  height: 660px;
  border-radius: 10px;
  border: 1px solid var(--border-subtle);
  background: #070b14;
  overflow: hidden;
  position: relative;
}
.graph-canvas-wrap {
  width: 100%;
  height: 100%;
  position: relative;
}
.graph-canvas {
  display: block;
  cursor: grab;
}
.graph-canvas:active {
  cursor: grabbing;
}
.graph-overlay {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
  pointer-events: none;
  font-size: 13px;
}
.graph-hud-tl {
  position: absolute;
  top: 10px;
  left: 12px;
  gap: 6px;
  flex-wrap: wrap;
}
.graph-hud-bl {
  position: absolute;
  bottom: 10px;
  left: 12px;
  font-size: 11px;
  pointer-events: none;
}
.hud-btn {
  padding: 4px 9px;
  font-size: 12px;
  background: rgba(15, 23, 42, 0.85);
  border: 1px solid var(--border-subtle);
  backdrop-filter: blur(6px);
}
.graph-panel-wrap {
  position: absolute;
  top: 10px;
  right: 12px;
  width: 240px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  z-index: 4;
}
.graph-panel {
  background: rgba(15, 23, 42, 0.92);
  border: 1px solid var(--border-strong);
  border-radius: 10px;
  padding: 8px;
  backdrop-filter: blur(10px);
  max-height: 550px;
  overflow-y: auto;
  gap: 6px;
}
.panel-sec {
  border-bottom: 1px solid var(--border-subtle);
  padding-bottom: 6px;
}
.panel-sec:last-child {
  border-bottom: none;
  padding-bottom: 0;
}
.sec-head {
  width: 100%;
  text-align: left;
  background: transparent;
  border: none;
  padding: 4px 2px;
  font-size: 12px;
  font-weight: 600;
  color: #e2e8f0;
}
.sec-body {
  padding: 6px 2px 2px;
  gap: 7px;
}
.group-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 180px;
  overflow-y: auto;
}
.group-chip {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  font-size: 12px;
  text-align: left;
  background: rgba(7, 11, 20, 0.5);
  border: 1px solid var(--border-subtle);
}
.group-chip.active {
  border-color: var(--accent-cyan);
  background: rgba(56, 189, 248, 0.14);
}
.group-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.slider-row {
  display: flex;
  flex-direction: column;
  gap: 3px;
  font-size: 11px;
  color: var(--text-secondary);
}
.graph-tooltip {
  position: absolute;
  pointer-events: none;
  background: rgba(15, 23, 42, 0.96);
  border: 1px solid var(--accent-cyan);
  border-radius: 8px;
  padding: 8px 10px;
  max-width: 240px;
  z-index: 6;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
}
.tooltip-title {
  font-weight: 600;
  font-size: 13px;
  color: #f8fafc;
}
</style>
