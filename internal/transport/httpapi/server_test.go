package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lagrangian-dev/lagrangian/internal/core"
	"github.com/lagrangian-dev/lagrangian/internal/domain"
	"github.com/lagrangian-dev/lagrangian/internal/platform/paths"
	"github.com/lagrangian-dev/lagrangian/internal/platform/secrets"
	"github.com/lagrangian-dev/lagrangian/internal/transport/httpapi"
)

func newTestServer(t *testing.T) (*httptest.Server, *core.Service) {
	t.Helper()
	home := t.TempDir()
	layout, err := paths.Resolve(home)
	if err != nil {
		t.Fatalf("paths.Resolve: %v", err)
	}
	sec, err := secrets.NewFileStore(layout.SecretsDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc, err := core.NewService(layout, layout.ConfigFile, sec)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	ts := httptest.NewServer(httpapi.NewServer(svc))
	t.Cleanup(ts.Close)
	return ts, svc
}

func doJSON(t *testing.T, method, url, token string, body any) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http do %s %s: %v", method, url, err)
	}
	return resp
}

func TestSetupAuthTaskProgressAttachmentAndOrbit(t *testing.T) {
	ts, svc := newTestServer(t)

	// 1. Unauthenticated web setup routes must NOT exist (CLI-only initialization).
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/setup/status"},
		{"POST", "/api/v1/setup/init"},
	} {
		resp := doJSON(t, tc.method, ts.URL+tc.path, "", map[string]any{})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", tc.method, tc.path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// 2. Before CLI init, protected API returns 428 Precondition Required.
	resp := doJSON(t, "GET", ts.URL+"/api/v1/tasks", "", nil)
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("before setup status = %d, want 428", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 3. Initialize internally (same code path as `lagrangian-server init`).
	initRes, err := svc.InitSetup(core.SetupInitInput{
		Mode:          "server",
		AuthEnabled:   true,
		AuthorAliases: []string{"dev.alice"},
	})
	if err != nil {
		t.Fatalf("InitSetup failed: %v", err)
	}
	if !strings.HasPrefix(initRes.GeneratedToken, "lg_") {
		t.Fatalf("expected generated token with lg_ prefix, got %q", initRes.GeneratedToken)
	}
	tok := initRes.GeneratedToken

	// 3. Unauthorized access rejected.
	resp = doJSON(t, "GET", ts.URL+"/api/v1/tasks", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 4. Create task.
	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks", tok, map[string]any{
		"title":       "Implement Lagrangian Core",
		"status":      "in_progress",
		"priority":    "high",
		"next_action": "Verify HTTP & SQLite",
		"tags":        []string{"phase-1", "go"},
	})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("create task status = %d, body=%s", resp.StatusCode, string(raw))
	}
	var created domain.Task
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode task: %v", err)
	}
	_ = resp.Body.Close()

	// 5. Add progress entry.
	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks/"+created.ID+"/progress", tok, map[string]any{
		"title":       "完成 SQLite 迁移",
		"body":        "已验证 WAL 模式与外键约束。",
		"occurred_on": "2026-11-26",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add progress status = %d", resp.StatusCode)
	}
	var prog domain.ProgressEntry
	_ = json.NewDecoder(resp.Body).Decode(&prog)
	_ = resp.Body.Close()

	// 6. Upload an attachment linked to the progress entry and download it back.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("progress_id", prog.ID)
	fw, _ := mw.CreateFormFile("file", "report.html")
	_, _ = fw.Write([]byte("<h1>Test Evidence</h1>"))
	_ = mw.Close()

	upReq, _ := http.NewRequest("POST", ts.URL+"/api/v1/tasks/"+created.ID+"/attachments", &buf)
	upReq.Header.Set("Content-Type", mw.FormDataContentType())
	upReq.Header.Set("Authorization", "Bearer "+tok)
	upResp, err := http.DefaultClient.Do(upReq)
	if err != nil || upResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload attachment err=%v status=%v", err, upResp.StatusCode)
	}
	var att domain.Attachment
	_ = json.NewDecoder(upResp.Body).Decode(&att)
	_ = upResp.Body.Close()

	dlResp := doJSON(t, "GET", ts.URL+att.DownloadURL, tok, nil)
	dlBody, _ := io.ReadAll(dlResp.Body)
	_ = dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK || string(dlBody) != "<h1>Test Evidence</h1>" {
		t.Fatalf("download attachment status=%d body=%q", dlResp.StatusCode, string(dlBody))
	}

	// 7. Workstations endpoint works cleanly with empty list.
	resp = doJSON(t, "GET", ts.URL+"/api/v1/workstations", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("workstations status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 8. Independent component config update (code_host) does not overwrite system settings.
	resp = doJSON(t, "PUT", ts.URL+"/api/v1/config/components/code_host", tok, map[string]any{
		"git": map[string]any{
			"author_aliases":        []string{"dev.alice", "alice@example.com"},
			"auto_reconcile_rebase": true,
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update code_host component status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 9. Orbit snapshot includes active task, host_os & component slots.
	resp = doJSON(t, "GET", ts.URL+"/api/v1/system/orbit", tok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("orbit status = %d", resp.StatusCode)
	}
	var snap domain.OrbitSnapshot
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	_ = resp.Body.Close()
	if snap.Stats.ActiveTasks != 1 || snap.Stats.TotalComponents == 0 || snap.HostOS == "" {
		t.Fatalf("unexpected orbit snapshot: %+v", snap)
	}
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=dev.alice",
		"GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=dev.alice",
		"GIT_COMMITTER_EMAIL=alice@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestGitRebaseReconciliationPreservesDistinctSameTitleCommits(t *testing.T) {
	ts, svc := newTestServer(t)

	if _, err := svc.InitSetup(core.SetupInitInput{
		Mode:          "standalone",
		AuthEnabled:   false,
		AuthorAliases: []string{"dev.alice"},
	}); err != nil {
		t.Fatalf("InitSetup failed: %v", err)
	}

	repoDir := t.TempDir()
	runGitCmd(t, repoDir, "init", "-b", "main")
	_ = os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("v1\n"), 0o644)
	runGitCmd(t, repoDir, "add", ".")
	runGitCmd(t, repoDir, "commit", "-m", "initial")

	runGitCmd(t, repoDir, "checkout", "-b", "feat/orbit")
	_ = os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("patch-a\n"), 0o644)
	runGitCmd(t, repoDir, "add", "a.txt")
	runGitCmd(t, repoDir, "commit", "-m", "refine orbit view")
	shaA := runGitCmd(t, repoDir, "rev-parse", "HEAD")

	_ = os.WriteFile(filepath.Join(repoDir, "b.txt"), []byte("patch-b\n"), 0o644)
	runGitCmd(t, repoDir, "add", "b.txt")
	runGitCmd(t, repoDir, "commit", "-m", "refine orbit view")
	shaB := runGitCmd(t, repoDir, "rev-parse", "HEAD")

	resp := doJSON(t, "POST", ts.URL+"/api/v1/tasks", "", map[string]any{
		"title":  "Orbit Refactor",
		"status": "in_progress",
	})
	var task domain.Task
	_ = json.NewDecoder(resp.Body).Decode(&task)
	_ = resp.Body.Close()

	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks/"+task.ID+"/repos", "", map[string]any{
		"repo_name":   "demo-repo",
		"repo_path":   repoDir,
		"branch_name": "feat/orbit",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bind repo failed: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = doJSON(t, "GET", ts.URL+"/api/v1/tasks/"+task.ID+"/commits/candidates", "", nil)
	var candPayload struct {
		Candidates []domain.CommitCandidate `json:"candidates"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&candPayload)
	_ = resp.Body.Close()

	var toBind []core.BindCommitInput
	for _, c := range candPayload.Candidates {
		if c.SHA == shaA || c.SHA == shaB {
			toBind = append(toBind, core.BindCommitInput{
				RepoName:    c.RepoName,
				BranchName:  c.BranchName,
				SHA:         c.SHA,
				Title:       c.Title,
				Author:      c.Author,
				CommittedAt: c.CommittedAt,
				PatchID:     c.PatchID,
			})
		}
	}
	if len(toBind) != 2 {
		t.Fatalf("expected 2 feature candidates, got %d", len(toBind))
	}
	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks/"+task.ID+"/commits", "", map[string]any{"commits": toBind})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind commits failed: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	runGitCmd(t, repoDir, "checkout", "main")
	_ = os.WriteFile(filepath.Join(repoDir, "main-update.txt"), []byte("upstream\n"), 0o644)
	runGitCmd(t, repoDir, "add", "main-update.txt")
	runGitCmd(t, repoDir, "commit", "-m", "upstream change")
	runGitCmd(t, repoDir, "checkout", "feat/orbit")
	runGitCmd(t, repoDir, "rebase", "main")

	resp = doJSON(t, "POST", ts.URL+"/api/v1/tasks/"+task.ID+"/commits/reconcile", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reconcile failed: %d", resp.StatusCode)
	}
	var rec domain.ReconcileResult
	_ = json.NewDecoder(resp.Body).Decode(&rec)
	_ = resp.Body.Close()

	if len(rec.Replaced) != 2 {
		t.Fatalf("expected 2 rebase replacements, got %+v", rec)
	}

	resp = doJSON(t, "GET", ts.URL+"/api/v1/tasks/"+task.ID, "", nil)
	var detail domain.TaskDetail
	_ = json.NewDecoder(resp.Body).Decode(&detail)
	_ = resp.Body.Close()

	if len(detail.Commits) != 2 {
		t.Fatalf("expected 2 active commits after rebase, got %d", len(detail.Commits))
	}
	for _, c := range detail.Commits {
		if c.SHA == shaA || c.SHA == shaB {
			t.Fatalf("old rebased SHA %s should have been superseded", c.SHA)
		}
	}
}
