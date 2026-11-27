# Phase 2 逐文件代码导读：外部平台可插拔集成与通用多知识库

本文档按「数据迁移 → 领域模型 → 外部平台适配器 → 知识库引擎 → 存储层 → 业务编排 → HTTP API → Vue 前端」的真实调用链逐文件拆解 Phase 2 实现，确保每一条请求与数据流均透明可追踪。

---

## 1. 数据模型与 SQLite 增量迁移

### `internal/storage/migrations/002_integrations_kb.sql`
在 Phase 1 表结构基础上增量创建 6 张表，通过既有 `schema_migrations` 机制自动平滑升级已有数据库，不破坏任何 Phase 1 数据：
- `integration_connections`：外部平台连接元数据（`kind`, `driver`, `base_url`, `auth_type`, `username`, `secret_ref`, `options_json`, `enabled`, `last_checked_at`, `last_error`）。**不存明文 Token/密码**，仅保存安全凭据库键名 `secret_ref`。
- `remote_links`：任务与远端 Jira 工单 / GitLab MR 的多对多关联，按 `(task_id, connection_id, remote_kind, remote_key)` 建唯一约束，任务删除时级联清理。
- `issue_followups`：纯本地工单跟踪状态（`active` / `paused` / `pending_retest` / `verified`）与备注，按 `(connection_id, issue_key)` 唯一。所有状态变更仅持久化在本地 SQLite，不回写远端 Jira。
- `knowledge_bases`：通用多知识库注册表（`source_type` 为 `local_dir` 或 `remote_git`，包含 `mirror_dir`、`last_commit_sha`、`last_synced_at`、`last_error`）。
- `knowledge_docs` + `knowledge_fts`（FTS5 虚拟表）：文档元数据（含内容 SHA-256 与体积）与全文检索倒排索引（使用 `unicode61` 分词器）。

### `internal/domain/phase2.go` & `internal/domain/models.go`
- 定义 `IntegrationConnection`、`RemoteLink`、`IssueFollowup`、`KnowledgeBase`、`KnowledgeDoc`、`KnowledgeDocContent`、`KnowledgeSearchHit`。
- 所有包含 `SecretRef` 的结构体字段均标记 `json:"-"`，接口仅返回布尔标志 `has_secret`，确保凭据键名和密文不会泄露到前端。
- `TaskDetail` 扩展 `RemoteLinks []RemoteLink`。

---

## 2. 可插拔外部平台适配器（零硬编码）

### `internal/components/codehost/gitlab/client.go`
实现 `components.CodeHost` 契约，对接标准 GitLab REST API v4：
- **认证**：支持 `pat`（`PRIVATE-TOKEN` 请求头）与 `oauth_password`（通过 `POST /oauth/token` 换取 `access_token` / `refresh_token` 并自动续期，以 `Authorization: Bearer` 调用 API；不保存明文密码，明确拒绝 GitLab REST API 不支持的账号密码 Basic Auth）。
- **`TestConnection`**：调用 `GET /api/v4/user` 验证连通性与当前账号身份。
- **`ListProjects`**：调用 `GET /api/v4/projects?membership=true&simple=true` 列出有权限项目。
- **`QueryMergeRequests`**：支持按项目路径或全局查询，支持 `scope`（`assigned_to_me` / `created_by_me` / `all`）、`state`、`search` 筛选。
- **`ListRemoteCommits`**：
  1. 先查 `GET /api/v4/projects/{path}/repository/commits?ref_name={branch}`；
  2. 当分支已被合并删除导致 `404` 或返回空集时，自动回退查询 `GET /api/v4/projects/{path}/merge_requests?state=merged&source_branch={branch}` 并抓取合并 MR 的提交列表；
  3. 根据连接级 `options.author_aliases`（未设则回退全局 `git.author_aliases`）按作者名/邮箱子串过滤。

### `internal/components/codehost/github/client.go`
实现 `components.CodeHost` 契约，对接标准 GitHub REST API v3（`https://api.github.com` 及 GitHub Enterprise Server）：
- **认证**：仅支持 `pat`（`Authorization: Bearer`），自动将 `https://github.com` 归一化为 `https://api.github.com`。
- **`TestConnection` / `ListProjects` / `QueryPullRequests` / `ListRemoteCommits`**：支持仓库列表查询、指定仓库 PR 列表与跨仓库 `/search/issues` 检索、分支及已删除分支对应已合并 PR 的提交回退查询与作者过滤，通过统一 `/api/v1/integrations/connections/{id}/codehost/{projects|mrs|commits}` 路由与前端无缝复用。

### `internal/components/issuetracker/jira/client.go`
实现 `components.IssueTracker` 契约，对接标准 Jira REST API v2：
- **认证**：支持 `pat`（`Authorization: Bearer`）与 `basic`（`Authorization: Basic`）。
- **`TestConnection`**：调用 `GET /rest/api/2/myself` 验证连通性。
- **`BuildJQL` / `SearchIssues`**：
  - 无任何硬编码项目名或中文状态名；
  - 从连接配置的 `options.default_project` 与 `options.inspection_statuses` 拼接 JQL（单次查询参数可按需覆盖）；
  - `assignee=me` 自动转为标准 JQL `assignee = currentUser()`；
  - 支持直接按工单号（如 `PROJ-123`）或摘要文本（`summary ~`）检索。

---

## 3. 通用多知识库与 FTS5 检索引擎

### `internal/components/knowledge/source.go`
- **`SyncRemoteGit` / `WriteAndPushRemoteGit`**：
  - 镜像目录固定在 `<data_dir>/kb-mirrors/<kb_id>`，支持按库配置只读或可写双向同步；
  - 若提供访问 Token，同步与推送期间动态创建权限 `0700` 的临时 `GIT_ASKPASS` 脚本（区分 `Username` 与密码提示，默认用户名 `oauth2` 兼容 GitLab/GitHub PAT）并在 `defer` 中立即删除，**绝不将凭据写入 URL 或 `.git/config`**；
  - 可写 `remote_git` 保存文档时自动执行 `git fetch` → 写盘 → `git commit` → 若远端超前则执行安全 `git rebase` → `git push`（无内容冲突自动变基合入，遇真实内容冲突自动 `rebase --abort` 并回滚本地提交，绝不强推覆盖）。
- **`ScanDocs` / `ReadDoc` / `WriteDoc`**：
  - 递归扫描 `.md` / `.markdown` / `.txt` 文件（自动跳过 `.git`、`node_modules`、`vendor` 等隐藏/依赖目录，单文件上限 2MB），提取首个一级标题或文件名作为 `Title`；
  - `safeJoin` 严格校验路径边界，拦截 `../` 目录穿越；
  - `WriteDoc` 仅允许非只读的 `local_dir` 知识库写入，写入后即时返回索引记录用于单文档增量更新 FTS5。

### `internal/storage/phase2.go`
- **`ReplaceKnowledgeDocs`**：在单个事务内原子替换某知识库的 `knowledge_docs` 与 `knowledge_fts` 记录。
- **`UpsertKnowledgeDoc`**：单文档保存后原子更新元数据与 FTS5 条目。
- **`SearchKnowledge`**：优先执行 `knowledge_fts MATCH ?` 并调用 `snippet(...)` 生成上下文摘要；针对中文/CJK 无空格连续短语自动触发 `LIKE` 子串补检，确保中英文关键词均可稳定命中。

---

## 4. 业务编排与 REST API 路由

### `internal/core/phase2.go` & `internal/core/service.go`
- 连接创建/更新时将 `Secret` 写入平台安全存储（macOS Keychain / Linux AES-256-GCM 加密文件），删除连接或知识库时同步清理对应凭据与 `kb-mirrors/<id>` 目录。
- `JiraSearchIssues` 将远端实时查询结果与本地 `issue_followups` 按 `issue_key` 合并返回。
- `livePhase2Statuses` 将已配置的真实 GitLab/Jira 连接和知识库动态注入 `GET /api/v1/components` 与 `GET /api/v1/system/orbit`（Ring 2 展示任务关联的远端工单/MR，Ring 3 展示各外部连接与知识库实时健康态）。

### `internal/transport/httpapi/phase2.go`
全部挂载在 Bearer Token 鉴权中间件 `s.auth(...)` 下：
- 外部连接：`GET/POST /api/v1/integrations/connections`、`PATCH/DELETE /api/v1/integrations/connections/{id}`、`POST /api/v1/integrations/connections/{id}/test`
- 远端实时查询：`GET /api/v1/integrations/connections/{id}/gitlab/{projects|mrs|commits}`、`GET /api/v1/integrations/connections/{id}/jira/issues`
- 本地跟踪与关联：`GET/PUT /api/v1/integrations/followups`、`POST/DELETE /api/v1/tasks/{id}/remote-links[/{lid}]`
- 知识库与检索：`GET/POST/DELETE /api/v1/knowledge/bases[/{id}]`、`POST /api/v1/knowledge/bases/{id}/sync`、`GET /api/v1/knowledge/bases/{id}/docs`、`GET/PUT /api/v1/knowledge/bases/{id}/doc`、`GET /api/v1/knowledge/search`

---

## 5. 前端视图集成

- `frontend/src/types/models.ts` & `frontend/src/api/client.ts`：补全 Phase 2 全量强类型模型与 API 客户端方法。
- `frontend/src/views/SettingsView.vue`：激活「Git 与代码托管」（GitLab 多实例管理）、「工单系统 (Jira)」（多实例与巡检状态白名单配置）、「通用知识库」（本地目录与远端 Git 知识库增删同步）三大真实配置面板。
- `frontend/src/views/IntegrationsView.vue`：新增「集成中心」视图，支持 Jira 工单查询、本地搁置/待复测/已验收标记、GitLab MR 检索与一键关联到任务。
- `frontend/src/views/KnowledgeView.vue`：新增「知识库」视图，支持跨知识库 FTS5 全文检索、点击命中摘要直达文档、多知识库切换同步、本地可写文档在线新建/编辑保存。
- `frontend/src/views/TasksView.vue`：任务详情展示关联外部工单/MR（支持跳转外链与解绑），提交绑定区新增「拉取 GitLab 远端提交」面板（支持分支及已删分支 MR 回退提交多选绑定）。
- `frontend/src/views/OrbitView.vue` & `frontend/src/App.vue`：顶栏接入「集成中心」「知识库」导航，轨道视图实时渲染中轨远端关联与外轨实时组件/知识库节点。

---

## 6. 自动化测试覆盖

### `internal/transport/httpapi/phase2_test.go`
- `TestPhase2GitLabJiraAndRemoteLinks`：使用 `httptest.NewServer` 仿真 GitLab REST v4 与 Jira REST v2，端到端验证连接凭据零明文落盘、连通性检测、已删分支 MR 提交回退查询、Jira JQL 构造与本地搁置状态合并、任务关联远端工单/MR 与星轨快照反射。
- `TestPhase2KnowledgeBasesLocalRemoteGitAndFTS5Search`：端到端验证本地目录知识库索引、在线编辑与实时索引更新、路径穿越（`../`）拦截、真实本地 Git 仓库作为 `remote_git` 镜像克隆/增量拉取同步、只读写入拦截、跨知识库中英文 FTS5 全文检索。
