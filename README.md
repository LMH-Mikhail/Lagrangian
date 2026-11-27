# Lagrangian

**Lagrangian** 是一个基于 **Go + Vue 3 + TypeScript** 的通用个人工程工作台，采用“稳定内核 + 可插拔组件 + 外部化 YAML 配置”架构，与特定公司、项目、硬件设备和操作系统解耦。

> 仓库中**不包含任何个人数据、账号别名或公司配置**。任何用户在克隆源码或安装发行包后，首次启动均通过终端分步向导 `lagrangian-server init` 完成初始化即可使用（不暴露无鉴权的网页初始化入口）。完整总体设计见根目录 [DESIGN.md](DESIGN.md)。

---

## 核心特性

1. **设备无关与全平台常驻支持**：
   - **常驻节点**：将 **Linux（`amd64`/`arm64` 单二进制、`systemd`、Docker、无头 AES-256-GCM 加密凭据库）**作为一等运行环境，同时完整支持 macOS 与 Windows。
   - **工作节点**：支持任意 1 到多台 macOS / Windows / Linux 工作机接入。
2. **前后端明确分离**：Go 后端（`cmd/` + `internal/`）与 Vue 3 + TypeScript 前端（`frontend/`）独立组织；发布构建时前端产物通过 `go:embed` 打入单个二进制文件。
3. **纯 Go 零 CGO 可移植性**：基于 `modernc.org/sqlite`，在任意机器均可一键交叉编译 Linux / macOS / Windows 服务端二进制。
4. **双视图工作台**：提供基于拉格朗日星轨隐喻的 **Lagrangian 轨道总览视图** 与高密度 **任务台账视图**。

---

## 仓库目录结构

```text
Lagrangian/
├── README.md                       # 本说明文档
├── DESIGN.md                       # 完整架构图、ER 模型与分阶段总体设计方案
├── Makefile                        # 构建、测试、开发联调、Linux/macOS/Windows 交叉编译
├── cmd/
│   └── lagrangian-server/          # 核心服务端入口（API + 内嵌 Vue 前端）
├── internal/
│   ├── config/                     # config.yaml 加载、校验、默认值与保存
│   ├── domain/                     # 纯领域模型（WorkstationNode, Task, Progress, Attachment, CommitLink, OrbitSnapshot）
│   ├── core/                       # 核心用例编排、Git 扫描与 patch-id Rebase 自动归并
│   ├── storage/                    # 纯 Go SQLite 持久化、内嵌 SQL 迁移与附件内容哈希落盘
│   ├── components/                 # 可插拔组件 Go Interface 契约（contracts.go）与注册中心（registry.go）
│   ├── transport/httpapi/          # REST API、Bearer Token 鉴权中间件与 SPA 静态托管
│   └── platform/                   # 跨平台路径（XDG/macOS/Windows）与凭据库（Keychain / AES-256-GCM）
├── frontend/                       # 独立 Vue 3 + TypeScript + Vite 前端工程
├── configs/
│   └── config.example.yaml         # 全量注释的配置模板（零个人数据）
├── packaging/
│   └── linux/                      # systemd 服务模板与多阶段 Dockerfile
└── docs/
    ├── architecture.md             # 分层依赖与组件扩展开发指南
    └── walkthroughs/               # 分阶段逐文件代码导读（彻底消除黑盒）
```

---

## 快速开始

### 环境要求
- Go `1.22+`
- Node.js `20+` 与 `pnpm`
- Git（用于本地分支提交扫描与 `git patch-id` Rebase 归并）

### 方式一：单二进制构建与终端分步初始化

```bash
make build
./bin/lagrangian-server init
./bin/lagrangian-server
```

或在终端直接运行 `./bin/lagrangian-server`（检测到无 `config.yaml` 且处于交互终端时，会自动进入 4 步终端初始化向导：部署模式 → 监听端口 → 数据目录 → Bearer Token 鉴权；监听地址按模式自动决定为 `127.0.0.1` 或 `0.0.0.0`，高级场景可通过启动参数 `--host` 覆盖）。完成后在浏览器访问对应地址（默认 `http://127.0.0.1:19000`）。

指定独立运行目录（方便隔离或换机迁移）：

```bash
./bin/lagrangian-server init --home /path/to/lagrangian-home
./bin/lagrangian-server --home /path/to/lagrangian-home
```

### 方式二：Linux 常驻节点部署（单二进制 / systemd / Docker）

```bash
# 1. 交叉编译 Linux 二进制（无需 CGO）
make build-linux-amd64
# 或 ARM64 服务器：
make build-linux-arm64

# 2. 将 dist/lagrangian-server-linux-amd64 拷贝到 Linux 主机 /usr/local/bin/lagrangian-server
# 3. 参考 packaging/linux/lagrangian-server.service 配置 systemd 开机自启
```

或直接使用容器构建：

```bash
docker build -f packaging/linux/Dockerfile -t lagrangian-server:latest .
docker run -d --name lagrangian -p 19000:19000 -v lagrangian_data:/data lagrangian-server:latest
```

### 方式三：前后端分离开发联调

```bash
# 终端 1：启动 Go 后端
make dev-backend

# 终端 2：启动 Vite 前端热更新（自动代理 /api 到 127.0.0.1:19000）
make dev-frontend
```

### 测试与全平台交叉编译

```bash
make test
make build-linux-amd64
make build-linux-arm64
make build-windows
```

---

## 分阶段进度与代码导读

| 阶段 | 核心范围 | 状态 | 代码导读 |
|---|---|---|---|
| **Phase 1** | 工程骨架、Linux/macOS/Windows 路径与凭据抽象、Setup Wizard、组件注册表、核心任务台账（进展/附件/多分支/提交/Rebase 归并）、Vue 3 轨道总览与台账界面、Linux `systemd`/Dockerfile 模板 | 已完成 | [phase-1-core.md](docs/walkthroughs/phase-1-core.md) |
| **Phase 2** | GitLab / Jira 可插拔组件（工单搁置/验收、远端分支与已删分支 MR 提交筛选、任务外链关联）、通用多知识库（本地目录可编辑 + 远端 Git `GIT_ASKPASS` 只读镜像 + SQLite FTS5 全文检索） | 已完成 | [phase-2-integrations-kb.md](docs/walkthroughs/phase-2-integrations-kb.md) |
| **Phase 3** | 跨平台工作节点代理 `lagrangian-agent`（macOS/Windows/Linux）、通用跨主机 MCP 网关 | 计划中 | — |
| **Phase 4** | 统一多 LLM 路由、可配置报告引擎、多企微机器人编排与 Outbox | 计划中 | — |
| **Phase 5** | Wails 跨平台桌面轻壳（本机/远端常驻节点切换）与多平台发行包 | 计划中 | — |
