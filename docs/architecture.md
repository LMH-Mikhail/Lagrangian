# Lagrangian 分层架构与组件扩展指南

完整总体架构图与分阶段路线图见根目录 [DESIGN.md](../DESIGN.md)。本文档聚焦代码层的单向依赖规则与组件开发约定。

## 1. 单向依赖规则

```text
cmd/lagrangian-server          (CLI 参数解析、依赖装配、优雅退出)
        │
        ▼
internal/transport/httpapi     (HTTP 路由、JSON 编解码、Bearer Token 鉴权、SPA 托管)
        │
        ▼
internal/core                  (业务校验、用例编排、Git 扫描与 Rebase 归并、轨道快照)
        │
   ┌────┼─────────────┐
   ▼    ▼             ▼
domain storage    components   (纯模型 / 纯 Go SQLite 持久化 / 可插拔组件契约与注册表)
                      │
                      ▼
               platform/config (Linux/macOS/Windows 路径与凭据库 / YAML 配置)
```

- `internal/domain` 零内部包依赖。
- `internal/storage` 仅依赖 `internal/domain` 与 `modernc.org/sqlite`，不写死业务状态流。
- `internal/transport/httpapi` 仅通过 `internal/core.Service` 访问数据与能力。
- `frontend/` 完全独立，只依赖 `/api/v1/*` JSON 契约。

---

## 2. 组件化扩展契约

所有外部能力接口统一声明在 `internal/components/contracts.go`：

- `CodeHost`：远端代码托管（GitLab / GitHub 等）
- `IssueTracker`：外部工单系统（Jira 等）
- `BotChannel`：多实例 IM 机器人通道（企微等，通过 `Roles()` 区分职责）
- `AgentSource`：本机或跨机工作节点 Agent 对话读取（Codex / Antigravity 等）
- `LLMProvider`：本地或远端大模型提供方（OpenAI 兼容 / Ollama / CLI）
- `KnowledgeSource`：本地目录或远端 Git 知识库

每个组件在 `config.yaml` 中通过统一 `ComponentSlot` 声明（`id`、`name`、`driver`、`enabled`、`secret_ref`、`options`），由 `components.Registry` 统一管理与健康上报。

---

## 3. 跨平台与安全边界

- **Linux 常驻节点一等支持**：
  - 路径遵循 XDG 规范（`$XDG_CONFIG_HOME/lagrangian`、`$XDG_DATA_HOME/lagrangian`）或 `--home` / `LAGRANGIAN_HOME`。
  - 凭据库使用 AES-256-GCM 加密文件（`0600` 权限），支持通过 `LAGRANGIAN_MASTER_KEY` 注入主密钥，完全不依赖 GUI 钥匙串。
- **macOS / Windows 支持**：
  - macOS 默认使用系统 Keychain（可通过 `LAGRANGIAN_SECRET_BACKEND=file` 切回无头加密文件）；
  - Windows 与 Linux 统一使用跨平台纯 Go 二进制与加密凭据库，零 CGO 编译依赖。
- **无明文凭据落盘**：`config.yaml` 与 SQLite 仅保存凭据引用名 `secret_ref`。
