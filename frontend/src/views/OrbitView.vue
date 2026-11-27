<script setup lang="ts">
import { computed, ref } from 'vue'
import type { OrbitNode, OrbitSnapshot } from '../types/models'

const props = defineProps<{
  snapshot: OrbitSnapshot | null
  loading: boolean
}>()

const emit = defineEmits<{
  (e: 'refresh'): void
  (e: 'open-task', taskId: string): void
  (e: 'open-settings'): void
}>()

const selected = ref<OrbitNode | null>(null)

const ringRadii: Record<number, number> = {
  1: 112,
  2: 186,
  3: 256,
}

interface PositionedNode extends OrbitNode {
  x: number
  y: number
}

const positionedNodes = computed<PositionedNode[]>(() => {
  if (!props.snapshot) return []
  const byRing: Record<number, OrbitNode[]> = { 1: [], 2: [], 3: [] }
  for (const node of props.snapshot.nodes) {
    byRing[node.ring]?.push(node)
  }
  const result: PositionedNode[] = []
  const cx = 310
  const cy = 290
  const offsets: Record<number, number> = { 1: -Math.PI / 2, 2: -Math.PI / 4, 3: 0 }

  for (const ring of [1, 2, 3]) {
    const items = byRing[ring]
    const r = ringRadii[ring]
    items.forEach((node, idx) => {
      const angle = offsets[ring] + (2 * Math.PI * idx) / Math.max(items.length, 1)
      result.push({
        ...node,
        x: cx + r * Math.cos(angle),
        y: cy + r * Math.sin(angle),
      })
    })
  }
  return result
})

const lagrangeMarkers = [
  { id: 'L1', x: 310, y: 178 },
  { id: 'L2', x: 310, y: 402 },
  { id: 'L3', x: 124, y: 290 },
  { id: 'L4', x: 472, y: 196 },
  { id: 'L5', x: 472, y: 384 },
]

function selectNode(node: PositionedNode) {
  selected.value = node
}
</script>

<template>
  <div class="orbit-layout">
    <section class="orbit-canvas card">
      <div class="row" style="justify-content: space-between; margin-bottom: 8px">
        <div>
          <h2 style="margin: 0; font-size: 16px">Lagrangian 轨道总览</h2>
          <span class="muted">内轨：活跃任务 · 中轨：仓库分支与远端工单/MR · 外轨：实时组件与知识库</span>
        </div>
        <button @click="emit('refresh')" :disabled="loading">
          {{ loading ? '同步中…' : '刷新星轨' }}
        </button>
      </div>

      <svg viewBox="0 0 620 580" class="orbit-svg" role="img" aria-label="Lagrangian 轨道视图">
        <defs>
          <radialGradient id="coreGlow" cx="50%" cy="50%" r="50%">
            <stop offset="0%" stop-color="#38bdf8" stop-opacity="0.55" />
            <stop offset="65%" stop-color="#6366f1" stop-opacity="0.18" />
            <stop offset="100%" stop-color="#070b14" stop-opacity="0" />
          </radialGradient>
        </defs>

        <circle cx="310" cy="290" r="256" class="ring ring-outer" />
        <circle cx="310" cy="290" r="186" class="ring ring-mid" />
        <circle cx="310" cy="290" r="112" class="ring ring-inner" />

        <g v-for="m in lagrangeMarkers" :key="m.id" opacity="0.45">
          <circle :cx="m.x" :cy="m.y" r="2.2" fill="#94a3b8" />
          <text :x="m.x + 5" :y="m.y + 3" class="l-marker">{{ m.id }}</text>
        </g>

        <circle cx="310" cy="290" r="64" fill="url(#coreGlow)" />
        <circle cx="310" cy="290" r="36" fill="#0f172a" stroke="#38bdf8" stroke-width="1.6" />
        <text x="310" y="285" text-anchor="middle" class="core-title">Lagrangian</text>
        <text x="310" y="300" text-anchor="middle" class="core-sub">
          {{ snapshot?.mode || 'standalone' }}
        </text>
        <text x="310" y="312" text-anchor="middle" class="core-os">
          {{ snapshot?.host_os || '' }}
        </text>

        <g
          v-for="node in positionedNodes"
          :key="node.id"
          class="orbit-node"
          :class="{ active: selected?.id === node.id }"
          @click="selectNode(node)"
        >
          <circle
            :cx="node.x"
            :cy="node.y"
            :r="node.ring === 1 ? 11 : node.ring === 2 ? 8.5 : 7.5"
            :fill="node.color"
            fill-opacity="0.22"
            :stroke="node.color"
            stroke-width="1.8"
          />
          <circle :cx="node.x" :cy="node.y" r="3.4" :fill="node.color" />
          <text :x="node.x" :y="node.y + 21" text-anchor="middle" class="node-label">
            {{ node.label }}
          </text>
        </g>
      </svg>
    </section>

    <aside class="orbit-inspector stack">
      <div class="card stats-grid" v-if="snapshot">
        <div class="stat">
          <span class="muted">进行中任务</span>
          <strong>{{ snapshot.stats.active_tasks }}</strong>
        </div>
        <div class="stat">
          <span class="muted">阻塞任务</span>
          <strong style="color: var(--accent-rose)">{{ snapshot.stats.blocked_tasks }}</strong>
        </div>
        <div class="stat">
          <span class="muted">绑定分支 / 有效提交</span>
          <strong>{{ snapshot.stats.bound_repos }} / {{ snapshot.stats.active_commits }}</strong>
        </div>
        <div class="stat">
          <span class="muted">已接入工作节点</span>
          <strong>{{ snapshot.stats.connected_workstations }}</strong>
        </div>
      </div>

      <div class="card stack" style="flex: 1">
        <h3 style="margin: 0; font-size: 14px">节点透视面板</h3>
        <template v-if="selected">
          <div class="row">
            <span class="dot" :style="{ background: selected.color }"></span>
            <strong>{{ selected.label }}</strong>
            <span class="badge">{{ selected.kind }}</span>
          </div>
          <p class="muted" style="margin: 0">{{ selected.subtitle }}</p>
          <div class="meta-list" v-if="selected.meta">
            <div v-for="(val, key) in selected.meta" :key="key" class="meta-row">
              <span class="muted">{{ key }}</span>
              <code>{{ val || '-' }}</code>
            </div>
          </div>
          <div class="row" style="margin-top: 8px">
            <button
              v-if="selected.kind === 'task' && selected.meta?.task_id"
              class="primary"
              @click="emit('open-task', selected.meta.task_id)"
            >
              进入任务详情
            </button>
            <button
              v-else-if="selected.kind === 'repo' && selected.meta?.task_id"
              @click="emit('open-task', selected.meta.task_id)"
            >
              查看所属任务
            </button>
            <a
              v-if="selected.meta?.web_url"
              :href="selected.meta.web_url"
              target="_blank"
              rel="noopener"
              class="badge"
              style="color: var(--accent-cyan); text-decoration: none; padding: 6px 10px"
            >
              打开外部链接 ↗
            </a>
            <button
              v-else-if="selected.kind === 'component'"
              @click="emit('open-settings')"
            >
              打开组件配置
            </button>
          </div>
        </template>
        <p v-else class="muted" style="margin: 0">
          点击左侧轨道上的任意任务、仓库分支或可插拔组件节点，可在此查看状态并快速跳转。
        </p>
      </div>
    </aside>
  </div>
</template>

<style scoped>
.orbit-layout {
  display: grid;
  grid-template-columns: minmax(420px, 1.45fr) minmax(280px, 0.85fr);
  gap: 14px;
  align-items: start;
}
.orbit-svg {
  width: 100%;
  max-height: 560px;
  display: block;
}
.ring {
  fill: none;
  stroke: rgba(148, 163, 184, 0.2);
  stroke-dasharray: 4 6;
}
.ring-inner {
  stroke: rgba(56, 189, 248, 0.34);
}
.ring-mid {
  stroke: rgba(167, 139, 250, 0.25);
}
.core-title {
  fill: #f8fafc;
  font-size: 10.5px;
  font-weight: 600;
}
.core-sub {
  fill: #94a3b8;
  font-size: 8.5px;
}
.core-os {
  fill: #64748b;
  font-size: 7.5px;
}
.l-marker {
  fill: #64748b;
  font-size: 9px;
}
.orbit-node {
  cursor: pointer;
}
.orbit-node:hover circle,
.orbit-node.active circle {
  filter: drop-shadow(0 0 6px currentColor);
}
.node-label {
  fill: #cbd5e1;
  font-size: 10px;
  pointer-events: none;
}
.stats-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.stat strong {
  font-size: 18px;
}
.meta-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  border-top: 1px solid var(--border-subtle);
  padding-top: 8px;
}
.meta-row {
  display: flex;
  justify-content: space-between;
  gap: 8px;
}
@media (max-width: 960px) {
  .orbit-layout {
    grid-template-columns: 1fr;
  }
}
</style>
