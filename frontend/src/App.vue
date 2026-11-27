<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { APIError, api, saveToken } from './api/client'
import type { AppConfig, OrbitSnapshot } from './types/models'
import OrbitView from './views/OrbitView.vue'
import IntegrationsView from './views/IntegrationsView.vue'
import KnowledgeView from './views/KnowledgeView.vue'
import SettingsView from './views/SettingsView.vue'
import TasksView from './views/TasksView.vue'

type Tab = 'orbit' | 'tasks' | 'integrations' | 'knowledge' | 'settings'

const loading = ref(true)
const appConfig = ref<AppConfig | null>(null)
const orbit = ref<OrbitSnapshot | null>(null)
const activeTab = ref<Tab>('orbit')
const focusTaskId = ref<string>('')
const needAuth = ref(false)
const tokenInput = ref('')
const authError = ref('')
const globalError = ref('')

onMounted(bootstrap)

async function bootstrap() {
  loading.value = true
  globalError.value = ''
  try {
    await loadWorkspace()
  } catch (err) {
    globalError.value = err instanceof Error ? err.message : '无法连接 Lagrangian 服务端'
  } finally {
    loading.value = false
  }
}

async function loadWorkspace() {
  needAuth.value = false
  authError.value = ''
  try {
    const [cfg, snap] = await Promise.all([api.getConfig(), api.orbitSnapshot()])
    appConfig.value = cfg
    orbit.value = snap
  } catch (err) {
    if (err instanceof APIError && err.status === 401) {
      needAuth.value = true
      return
    }
    throw err
  }
}

async function submitToken() {
  if (!tokenInput.value.trim()) return
  saveToken(tokenInput.value)
  tokenInput.value = ''
  try {
    await loadWorkspace()
    if (needAuth.value) {
      authError.value = '访问令牌无效，请核对后重试。'
    }
  } catch (err) {
    authError.value = err instanceof Error ? err.message : '验证失败'
  }
}

async function refreshOrbit() {
  try {
    orbit.value = await api.orbitSnapshot()
  } catch {
    // ignore transient refresh failure
  }
}

function logout() {
  saveToken('')
  appConfig.value = null
  orbit.value = null
  needAuth.value = true
  authError.value = ''
}

function openTask(taskId: string) {
  focusTaskId.value = taskId
  activeTab.value = 'tasks'
}
</script>

<template>
  <div v-if="loading" class="center-screen muted">正在连接 Lagrangian Core…</div>

  <div v-else-if="needAuth" class="center-screen">
    <div class="card stack" style="width: 390px">
      <div style="font-size: 11px; letter-spacing: 0.14em; color: var(--accent-cyan)">
        LAGRANGIAN · ACCESS CONTROL
      </div>
      <h2 style="margin: 0">输入访问令牌</h2>
      <p class="muted" style="margin: 0">
        当前 Lagrangian 实例已启用 Bearer Token 接口鉴权，请输入在终端执行
        <code>lagrangian-server init</code> 时生成的访问令牌。
      </p>
      <div v-if="authError" class="error-banner">{{ authError }}</div>
      <input v-model="tokenInput" type="password" placeholder="lg_..." @keyup.enter="submitToken" />
      <button class="primary" @click="submitToken">验证并进入</button>
    </div>
  </div>

  <div v-else-if="appConfig" class="app-shell">
    <header class="topbar">
      <div class="row">
        <span class="brand-dot"></span>
        <strong style="letter-spacing: 0.04em">Lagrangian</strong>
        <span class="badge">{{ appConfig.server.mode }}</span>
        <span v-if="orbit?.host_os" class="badge">{{ orbit.host_os }}</span>
      </div>
      <nav class="row">
        <button :class="{ primary: activeTab === 'orbit' }" @click="activeTab = 'orbit'">
          星轨总览
        </button>
        <button :class="{ primary: activeTab === 'tasks' }" @click="activeTab = 'tasks'">
          任务台账
        </button>
        <button :class="{ primary: activeTab === 'integrations' }" @click="activeTab = 'integrations'">
          集成中心
        </button>
        <button :class="{ primary: activeTab === 'knowledge' }" @click="activeTab = 'knowledge'">
          知识库
        </button>
        <button :class="{ primary: activeTab === 'settings' }" @click="activeTab = 'settings'">
          配置与组件
        </button>
        <button
          v-if="appConfig.server.auth_enabled"
          class="ghost danger"
          style="margin-left: 6px"
          @click="logout"
        >
          退出登录
        </button>
      </nav>
    </header>

    <main class="main-content">
      <div v-if="globalError" class="error-banner" style="margin-bottom: 12px">{{ globalError }}</div>

      <OrbitView
        v-if="activeTab === 'orbit'"
        :snapshot="orbit"
        :loading="false"
        @refresh="refreshOrbit"
        @open-task="openTask"
        @open-settings="activeTab = 'settings'"
      />

      <TasksView
        v-else-if="activeTab === 'tasks'"
        :config="appConfig"
        :initial-task-id="focusTaskId"
        @changed="refreshOrbit"
      />

      <IntegrationsView
        v-else-if="activeTab === 'integrations'"
        @changed="refreshOrbit"
        @open-settings="activeTab = 'settings'"
      />

      <KnowledgeView
        v-else-if="activeTab === 'knowledge'"
        @changed="refreshOrbit"
        @open-settings="activeTab = 'settings'"
      />

      <SettingsView
        v-else
        :config="appConfig"
        @updated="(cfg) => { appConfig = cfg; refreshOrbit() }"
        @changed="refreshOrbit"
      />
    </main>
  </div>

  <div v-else class="center-screen">
    <div class="error-banner">{{ globalError || '无法加载 Lagrangian 工作台' }}</div>
  </div>
</template>

<style scoped>
.center-screen {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 20px;
}
.app-shell {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 20px;
  border-bottom: 1px solid var(--border-subtle);
  background: rgba(11, 17, 32, 0.85);
  backdrop-filter: blur(8px);
  position: sticky;
  top: 0;
  z-index: 10;
}
.brand-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: radial-gradient(circle, #38bdf8, #6366f1);
  box-shadow: 0 0 10px #38bdf8;
}
.main-content {
  padding: 16px 20px;
  max-width: 1440px;
  width: 100%;
  margin: 0 auto;
}
</style>
