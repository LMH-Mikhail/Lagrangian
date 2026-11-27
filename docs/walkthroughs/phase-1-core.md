# Phase 1 代码导读：从启动到核心台账全链路

本导读按代码实际执行路径拆解 Phase 1 的每一个模块，阅读完以下 6 个环节即可完整掌控当前全部实现。

---

## 1. 启动装配与跨平台隔离

入口：`cmd/lagrangian-server/main.go`

1. 解析 `--home`、`--config`、`--host`、`--port`、`--dev`。
2. `paths.Resolve(*homeFlag)`（`internal/platform/paths/paths.go`）按当前 OS 规则计算目录：
   - Linux：`$XDG_CONFIG_HOME/lagrangian` 与 `$XDG_DATA_HOME/lagrangian`；
   - macOS：`~/Library/Application Support/Lagrangian`；
   - Windows：`%APPDATA%\Lagrangian`；
   - 任意平台均可通过 `--home` 或 `LAGRANGIAN_HOME` 强制集中到指定目录。
3. `secrets.Open(layout.SecretsDir)` 打开凭据库（Linux/Windows 使用 `file_store.go` 的 AES-256-GCM 加密文件；macOS 默认使用 `keychain_darwin.go`，支持设 `LAGRANGIAN_SECRET_BACKEND=file` 切为无头模式）。
4. `core.NewService()` 读取 `config.yaml`：不存在时进入 `setup_wizard` 状态，存在时打开 SQLite 并执行迁移。
5. `httpapi.NewServer(svc)` 挂载路由并监听 `SIGINT/SIGTERM` 优雅关闭。

---

## 2. 终端分步初始化与 Bearer Token 鉴权

为兼容 Linux 无头常驻部署并彻底消除未初始化网页被公网/局域网抢注的风险，Lagrangian **不暴露网页版 Setup 入口**，统一通过终端分步向导完成初始化（`internal/cli/init_wizard.go`）：

- 运行 `lagrangian-server init`（或首次在交互终端直接运行 `lagrangian-server`）依次完成 4 步：
  1. 部署模式（`standalone` 自动绑定 `127.0.0.1`、免密；`server` 自动绑定 `0.0.0.0`、默认开启鉴权）；
  2. 监听端口（高级网卡绑定场景可通过启动参数 `--host` 覆盖）；
  3. 数据目录；
  4. Bearer Token 接口鉴权（开启且留空时自动生成 `lg_` 随机高强度令牌，明文存入凭据库并在终端展示一次，`config.yaml` 仅保存 `api_token_ref: "server.api_token"`）。
- 在 `systemd` / Docker 等无 TTY 后台环境中，若无配置文件则直接报错退出并提示先运行 `lagrangian-server init`。
- 浏览器访问已开启鉴权的实例时，若收到 `401 Unauthorized`，`App.vue` 自动展示访问令牌输入框，验证通过后将令牌保存在浏览器会话中。

---

## 3. SQLite 持久化与版本化迁移

- Schema：`internal/storage/migrations/001_init.sql`（包含 `workstation_nodes`、`tasks`、`task_progress`、`attachments`、`repo_bindings`、`commit_links` 6 张表）。
- `internal/storage/sqlite.go` 通过 `//go:embed migrations/*.sql` 内嵌 SQL 文件，启动时自动对比 `schema_migrations` 并在事务中顺序执行新迁移。
- 使用 `modernc.org/sqlite` 纯 Go 驱动，开启 `WAL`、`foreign_keys(ON)`、`busy_timeout(5000)`，无需 CGO 即可交叉编译 Linux / macOS / Windows。

---

## 4. 任务、进展与附件存储链路

1. 前端 `TasksView.vue` 提交进展 → `POST /api/v1/tasks/{id}/progress` → `core.Service.AddProgress()` 写入 `task_progress`（标题留空时自动截取正文首行）。
2. 上传或剪贴板粘贴文件 → `POST /api/v1/tasks/{id}/attachments`（`multipart/form-data`，上限 25MB）。
3. `storage.Store.SaveAttachment()` 流式写入临时文件并同步计算 `SHA-256`，清洗文件名后原子移动至 `<attachments_dir>/<task_id>/<sha12>_<safe_name>` 并入库。
4. 下载接口 `GET /api/v1/attachments/{aid}` 对图片/PDF/纯文本设置 `inline` 预览，其余文件设置 `attachment` 下载。

---

## 5. 本地 Git 扫描与 Rebase 自动归并原理

实现位置：`internal/core/git_scanner.go` + `internal/core/service.go`（`ReconcileTaskRebases`）

1. 读取绑定分支提交时，按配置中的 `git.author_aliases` 过滤作者，并对每条提交调用 `git show --format= --patch <sha> | git patch-id --stable` 计算补丁指纹 `patch_id`。
2. 触发 `POST /api/v1/tasks/{id}/commits/reconcile` 时：
   - 收集任务下在当前分支已消失的已绑定旧 SHA，按 `patch_id` 分组；
   - **仅当某 `patch_id` 下恰好 1 条旧提交消失，且分支上恰好 1 条未绑定的新 SHA 具有完全一致的 `patch_id`** 时，在事务 `SupersedeCommit()` 中把旧记录标记 `superseded_by = new_sha` 并让新 SHA 继承原任务归属；
   - 标题相同但改动独立的提交具有不同的 `patch_id`，各自完整保留；rebase 解决冲突导致补丁变化时也不会误归并。

端到端真实 Git 仓库测试见 `internal/transport/httpapi/server_test.go` 的 `TestGitRebaseReconciliationPreservesDistinctSameTitleCommits`。

---

## 6. Vue 3 前端星轨视图与内嵌打包

- 前端位于独立目录 `frontend/`，类型定义 `src/types/models.ts` 与 Go `internal/domain/models.go` 字段一一对应。
- `OrbitView.vue` 请求 `GET /api/v1/system/orbit`，将后端返回的三层节点（Ring 1 活跃任务、Ring 2 仓库分支、Ring 3 组件注册状态）与 `L1–L5` 拉格朗日参考点绘制在 SVG 同心轨道中，并在引力中心展示当前节点运行模式与 OS/架构。
- 发布时 `make build` 将 `frontend/dist/` 复制到 `internal/transport/httpapi/webdist/dist/` 并通过 `go:embed` 打入单个 `lagrangian-server` 二进制文件。
