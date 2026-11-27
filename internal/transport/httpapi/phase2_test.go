package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lagrangian-dev/lagrangian/internal/components"
	"github.com/lagrangian-dev/lagrangian/internal/core"
	"github.com/lagrangian-dev/lagrangian/internal/domain"
)

func TestPhase2GitLabJiraAndRemoteLinks(t *testing.T) {
	// 1. Mock GitLab REST API v4 (+ OAuth2 password grant endpoint)
	gitlabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.FormValue("grant_type") == "password" && r.FormValue("username") == "dev.alice" && r.FormValue("password") == "pass-123" {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "oauth-access-abc",
					"refresh_token": "oauth-refresh-def",
					"expires_in":    7200,
				})
				return
			}
			http.Error(w, "invalid_grant", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-secret-xyz" && r.Header.Get("Authorization") != "Bearer oauth-access-abc" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v4/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"username": "dev.alice"})
		case r.URL.Path == "/api/v4/projects":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 101, "name": "core-api", "path_with_namespace": "team/core-api", "default_branch": "main", "web_url": "https://gitlab.example/team/core-api"},
			})
		case r.URL.Path == "/api/v4/merge_requests":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"iid": 42, "title": "Add Phase 2 adapters", "state": "opened",
					"source_branch": "feat/p2", "target_branch": "main",
					"web_url": "https://gitlab.example/team/core-api/-/merge_requests/42",
					"references": map[string]string{"full": "team/core-api!42"},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/repository/commits") && r.URL.Query().Get("ref_name") == "feat/deleted":
			// Simulate branch deleted after MR merge -> triggers merged MR fallback
			http.Error(w, "404 Branch Not Found", http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/merge_requests") && r.URL.Query().Get("source_branch") == "feat/deleted":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"iid": 99}})
		case strings.HasSuffix(r.URL.Path, "/merge_requests/99/commits"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "aaa111222333", "title": "Fix deleted branch commit", "author_name": "dev.alice", "author_email": "alice@example.com", "committed_date": "2026-11-27T01:00:00Z"},
				{"id": "bbb444555666", "title": "Someone else commit", "author_name": "other.bob", "author_email": "bob@example.com", "committed_date": "2026-11-27T01:05:00Z"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gitlabSrv.Close)

	// 2. Mock Jira REST API v2
	var capturedJQL string
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "alice" || p != "jira-pass-999" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/2/myself":
			_ = json.NewEncoder(w).Encode(map[string]string{"displayName": "Alice Dev"})
		case "/rest/api/2/status":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"name": "开放"},
				{"name": "处理中"},
				{"name": "重新打开"},
			})
		case "/rest/api/2/search":
			capturedJQL = r.URL.Query().Get("jql")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issues": []map[string]any{
					{
						"key": "ENG-101",
						"fields": map[string]any{
							"summary":  "Support pluggable Jira adapter",
							"status":   map[string]string{"name": "In Progress"},
							"assignee": map[string]string{"displayName": "Alice Dev"},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(jiraSrv.Close)

	ts, svc := newTestServer(t)
	if _, err := svc.InitSetup(core.SetupInitInput{Mode: "standalone", AuthEnabled: false}); err != nil {
		t.Fatalf("InitSetup: %v", err)
	}

	// GitLab must reject unsupported Basic Auth (username/password) on REST API v4
	resp := doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "code_host",
		"driver":    "gitlab",
		"name":      "Bad Basic GitLab",
		"base_url":  gitlabSrv.URL,
		"auth_type": "basic",
		"username":  "dev.alice",
		"secret":    "pass-123",
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when creating gitlab connection with basic auth, got %d", resp.StatusCode)
	}

	// GitLab OAuth2 password grant (/oauth/token -> Bearer token)
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "code_host",
		"driver":    "gitlab",
		"name":      "OAuth GitLab",
		"base_url":  gitlabSrv.URL,
		"auth_type": "oauth_password",
		"username":  "dev.alice",
		"secret":    "pass-123",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create gitlab oauth conn status=%d", resp.StatusCode)
	}
	var oauthConn domain.IntegrationConnection
	_ = json.NewDecoder(resp.Body).Decode(&oauthConn)
	_ = resp.Body.Close()
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections/"+oauthConn.ID+"/test", "", nil)
	var oauthTest core.ConnectionTestResult
	_ = json.NewDecoder(resp.Body).Decode(&oauthTest)
	_ = resp.Body.Close()
	if !oauthTest.OK || oauthTest.Identity != "dev.alice" {
		t.Fatalf("gitlab oauth test failed: %+v", oauthTest)
	}

	// Create GitLab PAT connection & test
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "code_host",
		"driver":    "gitlab",
		"name":      "Mock GitLab",
		"base_url":  gitlabSrv.URL,
		"auth_type": "pat",
		"secret":    "glpat-secret-xyz",
		"options":   map[string]string{"author_aliases": "dev.alice"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create gitlab conn status=%d", resp.StatusCode)
	}
	var glConn domain.IntegrationConnection
	_ = json.NewDecoder(resp.Body).Decode(&glConn)
	_ = resp.Body.Close()
	if !glConn.HasSecret {
		t.Fatalf("expected has_secret=true")
	}

	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections/"+glConn.ID+"/test", "", nil)
	var testRes core.ConnectionTestResult
	_ = json.NewDecoder(resp.Body).Decode(&testRes)
	_ = resp.Body.Close()
	if !testRes.OK || testRes.Identity != "dev.alice" {
		t.Fatalf("gitlab test failed: %+v", testRes)
	}

	// Query GitLab remote commits for deleted branch -> verifies merged MR fallback + author filter
	resp = doJSON(t, "GET", ts.URL+"/api/v1/integrations/connections/"+glConn.ID+"/gitlab/commits?project=team/core-api&branch=feat/deleted", "", nil)
	var cmPayload struct {
		Commits []components.RemoteCommit `json:"commits"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cmPayload)
	_ = resp.Body.Close()
	if len(cmPayload.Commits) != 1 || cmPayload.Commits[0].SHA != "aaa111222333" {
		t.Fatalf("expected 1 filtered commit from merged MR fallback, got %+v", cmPayload.Commits)
	}

	// Pre-save connectivity test (draft) for Jira returns identity + localized statuses without persisting
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections/test", "", map[string]any{
		"kind":      "issue_tracker",
		"driver":    "jira",
		"name":      "Draft Jira",
		"base_url":  jiraSrv.URL,
		"auth_type": "basic",
		"username":  "alice",
		"secret":    "jira-pass-999",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("draft test status=%d", resp.StatusCode)
	}
	var draftRes core.ConnectionTestResult
	_ = json.NewDecoder(resp.Body).Decode(&draftRes)
	_ = resp.Body.Close()
	if !draftRes.OK || draftRes.Identity != "Alice Dev" || len(draftRes.Statuses) != 3 || draftRes.Statuses[0] != "开放" {
		t.Fatalf("unexpected jira draft test result: %+v", draftRes)
	}

	// Direct create with invalid credentials must fail connectivity check (400) and persist nothing
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "issue_tracker",
		"driver":    "jira",
		"name":      "Bad Jira",
		"base_url":  jiraSrv.URL,
		"auth_type": "basic",
		"username":  "alice",
		"secret":    "wrong-password",
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 when creating jira connection with invalid secret, got %d", resp.StatusCode)
	}

	// Create Jira connection with configurable status whitelist & test
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "issue_tracker",
		"driver":    "jira",
		"name":      "Mock Jira",
		"base_url":  jiraSrv.URL,
		"auth_type": "basic",
		"username":  "alice",
		"secret":    "jira-pass-999",
		"options":   map[string]string{"inspection_statuses": "Open, In Progress"},
	})
	var jiraConn domain.IntegrationConnection
	_ = json.NewDecoder(resp.Body).Decode(&jiraConn)
	_ = resp.Body.Close()

	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections/"+jiraConn.ID+"/test", "", nil)
	_ = json.NewDecoder(resp.Body).Decode(&testRes)
	_ = resp.Body.Close()
	if !testRes.OK || testRes.Identity != "Alice Dev" {
		t.Fatalf("jira test failed: %+v", testRes)
	}

	// Edit existing Jira connection: testConnectionDraft with connection_id & blank secret reuses stored secret
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections/test", "", map[string]any{
		"connection_id": jiraConn.ID,
		"kind":          "issue_tracker",
		"driver":        "jira",
		"base_url":      jiraSrv.URL,
		"auth_type":     "basic",
		"username":      "alice",
		"secret":        "",
	})
	var editTestRes core.ConnectionTestResult
	_ = json.NewDecoder(resp.Body).Decode(&editTestRes)
	_ = resp.Body.Close()
	if !editTestRes.OK || len(editTestRes.Statuses) != 3 {
		t.Fatalf("edit draft test reusing stored secret failed: %+v", editTestRes)
	}

	// Updating with invalid secret must fail connectivity check (400)
	resp = doJSON(t, "PATCH", ts.URL+"/api/v1/integrations/connections/"+jiraConn.ID, "", map[string]any{
		"secret": "wrong-updated-pass",
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on PATCH with invalid secret, got %d", resp.StatusCode)
	}

	// Updating name/options without touching secret must succeed
	resp = doJSON(t, "PATCH", ts.URL+"/api/v1/integrations/connections/"+jiraConn.ID, "", map[string]any{
		"name":    "Renamed Jira",
		"options": map[string]string{"inspection_statuses": "Open, In Progress"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on PATCH metadata edit, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Set local pause followup for ENG-101, then query Jira issues and verify local_state is merged
	resp = doJSON(t, "PUT", ts.URL+"/api/v1/integrations/followups", "", map[string]any{
		"connection_id": jiraConn.ID,
		"issue_key":     "ENG-101",
		"local_state":   "paused",
		"reason":        "Waiting for PM confirmation",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set followup status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doJSON(t, "GET", ts.URL+"/api/v1/integrations/connections/"+jiraConn.ID+"/jira/issues?assignee=me", "", nil)
	var issuePayload struct {
		Issues []core.JiraIssueWithFollowup `json:"issues"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&issuePayload)
	_ = resp.Body.Close()
	if !strings.Contains(capturedJQL, `status in ("Open", "In Progress")`) || !strings.Contains(capturedJQL, "assignee = currentUser()") {
		t.Fatalf("unexpected generated JQL: %s", capturedJQL)
	}
	if len(issuePayload.Issues) != 1 || issuePayload.Issues[0].LocalState != "paused" {
		t.Fatalf("expected paused local_state on ENG-101, got %+v", issuePayload.Issues)
	}

	// Link ENG-101 to a task and confirm it appears in task detail and OrbitSnapshot
	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks", "", map[string]any{"title": "Phase 2 Task", "status": "in_progress"})
	var task domain.Task
	_ = json.NewDecoder(resp.Body).Decode(&task)
	_ = resp.Body.Close()

	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks/"+task.ID+"/remote-links", "", map[string]any{
		"connection_id": jiraConn.ID,
		"remote_kind":   "issue",
		"remote_key":    "ENG-101",
		"title":         "Support pluggable Jira adapter",
		"remote_state":  "In Progress",
		"web_url":       jiraSrv.URL + "/browse/ENG-101",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("link remote status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doJSON(t, "GET", ts.URL+"/api/v1/tasks/"+task.ID, "", nil)
	var detail domain.TaskDetail
	_ = json.NewDecoder(resp.Body).Decode(&detail)
	_ = resp.Body.Close()
	if len(detail.RemoteLinks) != 1 || detail.RemoteLinks[0].RemoteKey != "ENG-101" {
		t.Fatalf("expected remote link in task detail, got %+v", detail.RemoteLinks)
	}
}

func TestPhase2KnowledgeBasesLocalRemoteGitAndFTS5Search(t *testing.T) {
	ts, svc := newTestServer(t)
	if _, err := svc.InitSetup(core.SetupInitInput{Mode: "standalone", AuthEnabled: false}); err != nil {
		t.Fatalf("InitSetup: %v", err)
	}

	// 1. Local writable knowledge base
	localDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(localDir, "architecture.md"), []byte("# 系统架构笔记\n\n使用 SQLite FTS5 实现全文检索。"), 0o644)

	resp := doJSON(t, "POST", ts.URL+"/api/v1/knowledge/bases", "", map[string]any{
		"name":        "Local Notes",
		"source_type": "local_dir",
		"location":    localDir,
		"readonly":    false,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create local kb status=%d", resp.StatusCode)
	}
	var localKB domain.KnowledgeBase
	_ = json.NewDecoder(resp.Body).Decode(&localKB)
	_ = resp.Body.Close()
	if localKB.DocCount != 1 {
		t.Fatalf("expected 1 doc in local KB, got %+v", localKB)
	}

	// Path traversal blocked on both PUT (write) and GET (read) with HTTP 400
	resp = doJSON(t, "PUT", ts.URL+"/api/v1/knowledge/bases/"+localKB.ID+"/doc", "", map[string]any{
		"path":    "../escape.md",
		"content": "bad",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for write path traversal, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doJSON(t, "GET", ts.URL+"/api/v1/knowledge/bases/"+localKB.ID+"/doc?path=../../etc/passwd", "", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for read path traversal, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Write a new doc with [[wikilink]] and verify live FTS5 search + per-KB KnowledgeGraph
	resp = doJSON(t, "PUT", ts.URL+"/api/v1/knowledge/bases/"+localKB.ID+"/doc", "", map[string]any{
		"path":    "guides/lagrangian.md",
		"content": "# Lagrangian 指南\n\n支持跨主机部署与组件化扩展，详见 [[architecture|系统架构]]。\n\n```md\n[[ignored-in-code]]\n```",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save doc status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doJSON(t, "GET", ts.URL+"/api/v1/knowledge/bases/"+localKB.ID+"/graph", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get knowledge graph status=%d", resp.StatusCode)
	}
	var kbGraph domain.KnowledgeGraph
	_ = json.NewDecoder(resp.Body).Decode(&kbGraph)
	_ = resp.Body.Close()
	if len(kbGraph.Nodes) != 2 || len(kbGraph.Edges) != 1 || kbGraph.Edges[0].Source != "guides/lagrangian.md" || kbGraph.Edges[0].Target != "architecture.md" {
		t.Fatalf("unexpected knowledge graph: %+v", kbGraph)
	}

	resp = doJSON(t, "GET", ts.URL+"/api/v1/knowledge/search?q=组件化扩展", "", nil)
	var searchRes struct {
		Hits []domain.KnowledgeSearchHit `json:"hits"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&searchRes)
	_ = resp.Body.Close()
	if len(searchRes.Hits) != 1 || searchRes.Hits[0].RelPath != "guides/lagrangian.md" {
		t.Fatalf("expected search hit for guides/lagrangian.md, got %+v", searchRes.Hits)
	}

	// 2. Remote Git knowledge base (readonly mirror)
	remoteRepo := t.TempDir()
	runGitCmd(t, remoteRepo, "init", "-b", "main")
	_ = os.WriteFile(filepath.Join(remoteRepo, "runbook.md"), []byte("# 故障排查手册\n\n检查网络链路与巡检游标。"), 0o644)
	runGitCmd(t, remoteRepo, "add", ".")
	runGitCmd(t, remoteRepo, "commit", "-m", "initial runbook")

	resp = doJSON(t, "POST", ts.URL+"/api/v1/knowledge/bases", "", map[string]any{
		"name":        "Remote Runbooks",
		"source_type": "remote_git",
		"location":    remoteRepo,
		"branch":      "main",
		"readonly":    true,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create remote git kb status=%d", resp.StatusCode)
	}
	var remoteKB domain.KnowledgeBase
	_ = json.NewDecoder(resp.Body).Decode(&remoteKB)
	_ = resp.Body.Close()
	if !remoteKB.ReadOnly || remoteKB.DocCount != 1 || remoteKB.LastCommitSHA == "" {
		t.Fatalf("unexpected remote git kb: %+v", remoteKB)
	}

	// Writing to readonly remote_git mirror must be rejected
	resp = doJSON(t, "PUT", ts.URL+"/api/v1/knowledge/bases/"+remoteKB.ID+"/doc", "", map[string]any{
		"path":    "runbook.md",
		"content": "overwrite attempt",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 writing to readonly remote_git kb, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Add a new commit to remoteRepo, trigger sync, verify new doc is indexed
	_ = os.WriteFile(filepath.Join(remoteRepo, "release.md"), []byte("# 发布规范\n\n零 CGO 交叉编译校验清单。"), 0o644)
	runGitCmd(t, remoteRepo, "add", "release.md")
	runGitCmd(t, remoteRepo, "commit", "-m", "add release checklist")

	resp = doJSON(t, "POST", ts.URL+"/api/v1/knowledge/bases/"+remoteKB.ID+"/sync", "", nil)
	_ = json.NewDecoder(resp.Body).Decode(&remoteKB)
	_ = resp.Body.Close()
	if remoteKB.DocCount != 2 {
		t.Fatalf("expected 2 docs after remote git sync, got %+v", remoteKB)
	}

	resp = doJSON(t, "GET", ts.URL+"/api/v1/knowledge/search?q=交叉编译", "", nil)
	_ = json.NewDecoder(resp.Body).Decode(&searchRes)
	_ = resp.Body.Close()
	if len(searchRes.Hits) != 1 || searchRes.Hits[0].KBID != remoteKB.ID {
		t.Fatalf("expected search hit in remote KB, got %+v", searchRes.Hits)
	}

	// 3. Writable Remote Git knowledge base: auto-commit + safe rebase + push + conflict rollback
	bareRemote := t.TempDir()
	runGitCmd(t, bareRemote, "init", "--bare", "-b", "main")
	peerWork := t.TempDir()
	runGitCmd(t, peerWork, "clone", bareRemote, ".")
	_ = os.WriteFile(filepath.Join(peerWork, "notes.md"), []byte("# 团队笔记\n\n第一行原始内容\n"), 0o644)
	runGitCmd(t, peerWork, "add", "notes.md")
	runGitCmd(t, peerWork, "commit", "-m", "seed notes")
	runGitCmd(t, peerWork, "push", "origin", "main")

	resp = doJSON(t, "POST", ts.URL+"/api/v1/knowledge/bases", "", map[string]any{
		"name":        "Writable Remote KB",
		"source_type": "remote_git",
		"location":    bareRemote,
		"branch":      "main",
		"readonly":    false,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create writable remote git kb status=%d", resp.StatusCode)
	}
	var wkb domain.KnowledgeBase
	_ = json.NewDecoder(resp.Body).Decode(&wkb)
	_ = resp.Body.Close()
	if wkb.ReadOnly || wkb.DocCount != 1 {
		t.Fatalf("unexpected writable remote kb: %+v", wkb)
	}

	// 3a. Save doc in Lagrangian -> auto commit & push to bareRemote
	resp = doJSON(t, "PUT", ts.URL+"/api/v1/knowledge/bases/"+wkb.ID+"/doc", "", map[string]any{
		"path":    "notes.md",
		"content": "# 团队笔记\n\n由 Lagrangian 服务端更新推送\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save writable remote doc status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Verify peerWork receives the pushed commit from bareRemote
	runGitCmd(t, peerWork, "pull", "--ff-only", "origin", "main")
	pushedBytes, _ := os.ReadFile(filepath.Join(peerWork, "notes.md"))
	if !strings.Contains(string(pushedBytes), "由 Lagrangian 服务端更新推送") {
		t.Fatalf("expected pushed content in remote repo, got %q", string(pushedBytes))
	}

	// 3b. Non-conflicting concurrent edit: peer pushes a different file `other.md`, Lagrangian saves `notes.md`
	_ = os.WriteFile(filepath.Join(peerWork, "other.md"), []byte("# 远端并发文件\n\n另一台机器写入的内容：跨机变基验证\n"), 0o644)
	runGitCmd(t, peerWork, "add", "other.md")
	runGitCmd(t, peerWork, "commit", "-m", "peer adds other.md")
	runGitCmd(t, peerWork, "push", "origin", "main")

	resp = doJSON(t, "PUT", ts.URL+"/api/v1/knowledge/bases/"+wkb.ID+"/doc", "", map[string]any{
		"path":    "notes.md",
		"content": "# 团队笔记\n\n安全变基后再次推送成功\n",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on non-conflicting auto-rebase push, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Confirm peerWork gets both other.md and updated notes.md, and FTS5 indexed other.md
	runGitCmd(t, peerWork, "pull", "--ff-only", "origin", "main")
	rebasedBytes, _ := os.ReadFile(filepath.Join(peerWork, "notes.md"))
	if !strings.Contains(string(rebasedBytes), "安全变基后再次推送成功") {
		t.Fatalf("expected rebased content in remote repo, got %q", string(rebasedBytes))
	}
	resp = doJSON(t, "GET", ts.URL+"/api/v1/knowledge/search?q=跨机变基验证&kb_id="+wkb.ID, "", nil)
	_ = json.NewDecoder(resp.Body).Decode(&searchRes)
	_ = resp.Body.Close()
	if len(searchRes.Hits) != 1 || searchRes.Hits[0].RelPath != "other.md" {
		t.Fatalf("expected auto-rebased other.md to be indexed in FTS5, got %+v", searchRes.Hits)
	}

	// 3c. Conflicting concurrent edit on same lines of notes.md -> must abort rebase, rollback, return 400
	_ = os.WriteFile(filepath.Join(peerWork, "notes.md"), []byte("# 团队笔记\n\n远端修改同一行制造冲突\n"), 0o644)
	runGitCmd(t, peerWork, "add", "notes.md")
	runGitCmd(t, peerWork, "commit", "-m", "peer conflicting edit")
	runGitCmd(t, peerWork, "push", "origin", "main")

	resp = doJSON(t, "PUT", ts.URL+"/api/v1/knowledge/bases/"+wkb.ID+"/doc", "", map[string]any{
		"path":    "notes.md",
		"content": "# 团队笔记\n\n本地修改同一行触发冲突拦截\n",
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on conflicting remote edit, got %d", resp.StatusCode)
	}
	// Verify remote bare repo was NOT overwritten by conflict
	runGitCmd(t, peerWork, "pull", "--ff-only", "origin", "main")
	stillRemote, _ := os.ReadFile(filepath.Join(peerWork, "notes.md"))
	if !strings.Contains(string(stillRemote), "远端修改同一行制造冲突") {
		t.Fatalf("remote repo must remain intact on conflict, got %q", string(stillRemote))
	}
}

func TestPhase2GitHubCodeHostAdapter(t *testing.T) {
	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ghp_test_token_123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "octocat", "name": "The Octocat"})
		case r.URL.Path == "/user/repos":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 7, "name": "lagrangian", "full_name": "octocat/lagrangian", "default_branch": "main", "html_url": "https://github.com/octocat/lagrangian"},
			})
		case r.URL.Path == "/repos/octocat/lagrangian/pulls" && r.URL.Query().Get("state") == "open":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"number": 15, "title": "Add GitHub CodeHost adapter", "state": "open",
					"html_url": "https://github.com/octocat/lagrangian/pull/15",
					"head":     map[string]string{"ref": "feat/github"},
					"base":     map[string]string{"ref": "main"},
				},
			})
		case r.URL.Path == "/repos/octocat/lagrangian/commits" && r.URL.Query().Get("sha") == "feat/merged-deleted":
			http.Error(w, "Not Found", http.StatusNotFound)
		case r.URL.Path == "/repos/octocat/lagrangian/pulls" && r.URL.Query().Get("state") == "closed":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"number": 12, "head": map[string]string{"ref": "feat/merged-deleted"}},
			})
		case r.URL.Path == "/repos/octocat/lagrangian/pulls/12/commits":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"sha": "ccc111222333", "html_url": "https://github.com/octocat/lagrangian/commit/ccc111222333",
					"commit": map[string]any{
						"message": "feat: add github adapter\n\nbody details",
						"author":  map[string]string{"name": "Octo Dev", "email": "octo@example.com", "date": "2026-11-27T06:00:00Z"},
					},
					"author": map[string]string{"login": "octocat"},
				},
				{
					"sha": "ddd444555666", "html_url": "https://github.com/octocat/lagrangian/commit/ddd444555666",
					"commit": map[string]any{
						"message": "chore: other author commit",
						"author":  map[string]string{"name": "Other User", "email": "other@example.com", "date": "2026-11-27T06:01:00Z"},
					},
					"author": map[string]string{"login": "otheruser"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ghSrv.Close)

	ts, svc := newTestServer(t)
	if _, err := svc.InitSetup(core.SetupInitInput{Mode: "standalone", AuthEnabled: false}); err != nil {
		t.Fatalf("InitSetup: %v", err)
	}

	// GitHub must reject basic auth
	resp := doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "code_host",
		"driver":    "github",
		"name":      "Bad GitHub",
		"base_url":  ghSrv.URL,
		"auth_type": "basic",
		"username":  "octocat",
		"secret":    "pw",
	})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for github basic auth, got %d", resp.StatusCode)
	}

	// Pre-save connectivity test + create GitHub PAT connection
	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections/test", "", map[string]any{
		"kind":      "code_host",
		"driver":    "github",
		"name":      "Personal GitHub",
		"base_url":  ghSrv.URL,
		"auth_type": "pat",
		"secret":    "ghp_test_token_123",
	})
	var draftRes core.ConnectionTestResult
	_ = json.NewDecoder(resp.Body).Decode(&draftRes)
	_ = resp.Body.Close()
	if !draftRes.OK || draftRes.Identity != "octocat" {
		t.Fatalf("github draft test failed: %+v", draftRes)
	}

	resp = doJSON(t, "POST", ts.URL+"/api/v1/integrations/connections", "", map[string]any{
		"kind":      "code_host",
		"driver":    "github",
		"name":      "Personal GitHub",
		"base_url":  ghSrv.URL,
		"auth_type": "pat",
		"secret":    "ghp_test_token_123",
		"options":   map[string]string{"author_aliases": "octocat"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create github conn status=%d", resp.StatusCode)
	}
	var ghConn domain.IntegrationConnection
	_ = json.NewDecoder(resp.Body).Decode(&ghConn)
	_ = resp.Body.Close()

	// List repos & PRs via unified /codehost/* routes
	resp = doJSON(t, "GET", ts.URL+"/api/v1/integrations/connections/"+ghConn.ID+"/codehost/projects?q=lagrangian", "", nil)
	var projRes struct {
		Projects []map[string]any `json:"projects"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&projRes)
	_ = resp.Body.Close()
	if len(projRes.Projects) != 1 || projRes.Projects[0]["path_with_namespace"] != "octocat/lagrangian" {
		t.Fatalf("unexpected github projects: %+v", projRes.Projects)
	}

	resp = doJSON(t, "GET", ts.URL+"/api/v1/integrations/connections/"+ghConn.ID+"/codehost/mrs?project=octocat/lagrangian&state=opened", "", nil)
	var prRes struct {
		MRs []components.MergeRequest `json:"merge_requests"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&prRes)
	_ = resp.Body.Close()
	if len(prRes.MRs) != 1 || prRes.MRs[0].ID != "octocat/lagrangian#15" {
		t.Fatalf("unexpected github PRs: %+v", prRes.MRs)
	}

	// Deleted branch PR commit fallback + author login filter
	resp = doJSON(t, "GET", ts.URL+"/api/v1/integrations/connections/"+ghConn.ID+"/codehost/commits?project=octocat/lagrangian&branch=feat/merged-deleted", "", nil)
	var cmRes struct {
		Commits []components.RemoteCommit `json:"commits"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cmRes)
	_ = resp.Body.Close()
	if len(cmRes.Commits) != 1 || cmRes.Commits[0].SHA != "ccc111222333" || cmRes.Commits[0].Title != "feat: add github adapter" {
		t.Fatalf("unexpected github fallback commits: %+v", cmRes.Commits)
	}
}
