// Package httpapi exposes Lagrangian's REST API, Bearer token authentication,
// Setup Wizard endpoints, and embedded Vue 3 SPA static asset hosting.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lagrangian-dev/lagrangian/internal/components"
	"github.com/lagrangian-dev/lagrangian/internal/config"
	"github.com/lagrangian-dev/lagrangian/internal/core"
	"github.com/lagrangian-dev/lagrangian/internal/storage"
	"github.com/lagrangian-dev/lagrangian/internal/transport/httpapi/webdist"
)

const maxUploadBytes = 25 << 20 // 25 MiB per attachment

type Server struct {
	svc    *core.Service
	mux    *http.ServeMux
	assets fs.FS
}

func NewServer(svc *core.Service) *Server {
	assets, _ := webdist.Assets()
	s := &Server{
		svc:    svc,
		mux:    http.NewServeMux(),
		assets: assets,
	}
	s.registerRoutes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) registerRoutes() {
	// Public health probe only (initialization is handled exclusively via terminal CLI)
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// Protected API routes
	s.mux.HandleFunc("GET /api/v1/config", s.auth(s.handleGetConfig))
	s.mux.HandleFunc("PUT /api/v1/config", s.auth(s.handleUpdateConfig))
	s.mux.HandleFunc("PATCH /api/v1/config/system", s.auth(s.handleUpdateSystemConfig))
	s.mux.HandleFunc("PUT /api/v1/config/components/{kind}", s.auth(s.handleUpdateComponentConfig))
	s.mux.HandleFunc("GET /api/v1/components", s.auth(s.handleListComponents))
	s.mux.HandleFunc("GET /api/v1/workstations", s.auth(s.handleListWorkstations))
	s.mux.HandleFunc("GET /api/v1/system/orbit", s.auth(s.handleOrbitSnapshot))

	s.mux.HandleFunc("GET /api/v1/tasks", s.auth(s.handleListTasks))
	s.mux.HandleFunc("POST /api/v1/tasks", s.auth(s.handleCreateTask))
	s.mux.HandleFunc("GET /api/v1/tasks/{id}", s.auth(s.handleGetTask))
	s.mux.HandleFunc("PATCH /api/v1/tasks/{id}", s.auth(s.handleUpdateTask))
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.auth(s.handleDeleteTask))

	s.mux.HandleFunc("POST /api/v1/tasks/{id}/progress", s.auth(s.handleAddProgress))
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}/progress/{pid}", s.auth(s.handleDeleteProgress))

	s.mux.HandleFunc("POST /api/v1/tasks/{id}/attachments", s.auth(s.handleUploadAttachment))
	s.mux.HandleFunc("GET /api/v1/attachments/{aid}", s.auth(s.handleDownloadAttachment))

	s.mux.HandleFunc("POST /api/v1/tasks/{id}/repos", s.auth(s.handleBindRepo))
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}/repos/{rid}", s.auth(s.handleUnbindRepo))

	s.mux.HandleFunc("GET /api/v1/tasks/{id}/commits/candidates", s.auth(s.handleCommitCandidates))
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/commits", s.auth(s.handleBindCommits))
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}/commits/{cid}", s.auth(s.handleUnbindCommit))
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/commits/reconcile", s.auth(s.handleReconcileRebases))

	// Phase 2: External integrations (GitLab/Jira) & generic multi-knowledge-base
	s.registerPhase2Routes()

	// SPA static frontend fallback
	s.mux.HandleFunc("/", s.handleSPA)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.svc.IsInitialized() {
			writeError(w, http.StatusPreconditionRequired, "setup_required", "请先完成首次初始化配置")
			return
		}
		token := extractBearer(r)
		if !s.svc.VerifyToken(token) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "无效或缺失访问令牌")
			return
		}
		next(w, r)
	}
}

func extractBearer(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return strings.TrimSpace(raw[7:])
	}
	if q := strings.TrimSpace(r.URL.Query().Get("token")); q != "" {
		return q
	}
	return ""
}

// --- Handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"service":     "lagrangian",
		"initialized": s.svc.IsInitialized(),
	})
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Config())
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var next config.Config
	if err := decodeJSON(r, &next); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	updated, err := s.svc.UpdateConfig(next)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleUpdateSystemConfig(w http.ResponseWriter, r *http.Request) {
	var patch core.SystemConfigPatch
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	updated, err := s.svc.UpdateSystemConfig(patch)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleUpdateComponentConfig(w http.ResponseWriter, r *http.Request) {
	var in core.ComponentConfigInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	kind := components.Kind(strings.TrimSpace(r.PathValue("kind")))
	updated, err := s.svc.UpdateComponentConfig(kind, in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleListComponents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"components": s.svc.ComponentStatuses(r.Context()),
	})
}

func (s *Server) handleListWorkstations(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.svc.ListWorkstations(r.Context())
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workstations": nodes})
}

func (s *Server) handleOrbitSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := s.svc.OrbitSnapshot(r.Context())
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := storage.TaskFilter{
		Status:          strings.TrimSpace(q.Get("status")),
		Search:          strings.TrimSpace(q.Get("q")),
		IncludeArchived: q.Get("include_archived") == "1" || q.Get("include_archived") == "true",
	}
	tasks, err := s.svc.ListTasks(r.Context(), filter)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var in core.CreateTaskInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	task, err := s.svc.CreateTask(r.Context(), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	detail, err := s.svc.GetTaskDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var in core.UpdateTaskInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	task, err := s.svc.UpdateTask(r.Context(), r.PathValue("id"), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteTask(r.Context(), r.PathValue("id")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleAddProgress(w http.ResponseWriter, r *http.Request) {
	var in core.AddProgressInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	entry, err := s.svc.AddProgress(r.Context(), r.PathValue("id"), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (s *Server) handleDeleteProgress(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteProgress(r.Context(), r.PathValue("id"), r.PathValue("pid")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "无效的文件上传请求或超过 25MB 限制")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "缺少 file 字段")
		return
	}
	defer file.Close()

	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		if ext := filepath.Ext(header.Filename); ext != "" {
			if detected := mime.TypeByExtension(ext); detected != "" {
				mimeType = detected
			}
		}
	}
	progressID := r.FormValue("progress_id")
	att, err := s.svc.UploadAttachment(r.Context(), r.PathValue("id"), progressID, header.Filename, mimeType, file)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

func (s *Server) handleDownloadAttachment(w http.ResponseWriter, r *http.Request) {
	att, fullPath, err := s.svc.GetAttachment(r.Context(), r.PathValue("aid"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	if _, statErr := os.Stat(fullPath); statErr != nil {
		writeError(w, http.StatusNotFound, "file_missing", "附件文件不存在")
		return
	}
	if att.MimeType != "" {
		w.Header().Set("Content-Type", att.MimeType)
	}
	disposition := "attachment"
	if strings.HasPrefix(att.MimeType, "image/") || att.MimeType == "application/pdf" || strings.HasPrefix(att.MimeType, "text/plain") {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, att.Filename))
	http.ServeFile(w, r, fullPath)
}

func (s *Server) handleBindRepo(w http.ResponseWriter, r *http.Request) {
	var in core.BindRepoInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	binding, err := s.svc.BindRepo(r.Context(), r.PathValue("id"), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, binding)
}

func (s *Server) handleUnbindRepo(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.UnbindRepo(r.Context(), r.PathValue("id"), r.PathValue("rid")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleCommitCandidates(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	candidates, warnings, err := s.svc.CommitCandidates(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": candidates,
		"warnings":   warnings,
	})
}

func (s *Server) handleBindCommits(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Commits []core.BindCommitInput `json:"commits"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	bound, skipped, err := s.svc.BindCommits(r.Context(), r.PathValue("id"), body.Commits)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bound":   bound,
		"skipped": skipped,
	})
}

func (s *Server) handleUnbindCommit(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.UnbindCommit(r.Context(), r.PathValue("id"), r.PathValue("cid")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleReconcileRebases(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.ReconcileTaskRebases(r.Context(), r.PathValue("id"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not_found", "API 路径不存在")
		return
	}
	if s.assets == nil {
		http.NotFound(w, r)
		return
	}
	clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if clean == "" || clean == "." {
		clean = "index.html"
	}
	if info, err := fs.Stat(s.assets, clean); err == nil && !info.IsDir() {
		http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<h1>Lagrangian Server</h1><p>Run <code>make build</code> or <code>make dev-frontend</code> to load the UI.</p>"))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// --- JSON Helpers ---

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("JSON 解析失败: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": msg,
		},
	})
}

func handleServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, core.ErrNotSetup):
		writeError(w, http.StatusPreconditionRequired, "setup_required", "请先完成首次初始化配置")
	case errors.Is(err, core.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, storage.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "记录不存在")
	case errors.Is(err, storage.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "记录已存在或发生冲突")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
