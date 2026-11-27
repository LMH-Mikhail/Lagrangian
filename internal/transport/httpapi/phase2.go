package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lagrangian-dev/lagrangian/internal/components"
	"github.com/lagrangian-dev/lagrangian/internal/core"
)

func (s *Server) registerPhase2Routes() {
	// External integration connections (GitLab / Jira)
	s.mux.HandleFunc("GET /api/v1/integrations/connections", s.auth(s.handleListConnections))
	s.mux.HandleFunc("POST /api/v1/integrations/connections", s.auth(s.handleCreateConnection))
	s.mux.HandleFunc("POST /api/v1/integrations/connections/test", s.auth(s.handleTestConnectionDraft))
	s.mux.HandleFunc("PATCH /api/v1/integrations/connections/{id}", s.auth(s.handleUpdateConnection))
	s.mux.HandleFunc("DELETE /api/v1/integrations/connections/{id}", s.auth(s.handleDeleteConnection))
	s.mux.HandleFunc("POST /api/v1/integrations/connections/{id}/test", s.auth(s.handleTestConnection))

	// CodeHost (GitLab / GitHub) & Jira live remote queries
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/codehost/projects", s.auth(s.handleGitLabProjects))
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/codehost/mrs", s.auth(s.handleGitLabMRs))
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/codehost/commits", s.auth(s.handleGitLabCommits))
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/gitlab/projects", s.auth(s.handleGitLabProjects))
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/gitlab/mrs", s.auth(s.handleGitLabMRs))
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/gitlab/commits", s.auth(s.handleGitLabCommits))
	s.mux.HandleFunc("GET /api/v1/integrations/connections/{id}/jira/issues", s.auth(s.handleJiraIssues))

	// Local issue followups (pause / pending_retest / verified) & task remote links
	s.mux.HandleFunc("GET /api/v1/integrations/followups", s.auth(s.handleListFollowups))
	s.mux.HandleFunc("PUT /api/v1/integrations/followups", s.auth(s.handleSetFollowup))
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/remote-links", s.auth(s.handleLinkRemote))
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}/remote-links/{lid}", s.auth(s.handleUnlinkRemote))

	// Generic multi-knowledge-base endpoints (local_dir + remote_git + FTS5 search)
	s.mux.HandleFunc("GET /api/v1/knowledge/bases", s.auth(s.handleListKnowledgeBases))
	s.mux.HandleFunc("POST /api/v1/knowledge/bases", s.auth(s.handleCreateKnowledgeBase))
	s.mux.HandleFunc("DELETE /api/v1/knowledge/bases/{id}", s.auth(s.handleDeleteKnowledgeBase))
	s.mux.HandleFunc("POST /api/v1/knowledge/bases/{id}/sync", s.auth(s.handleSyncKnowledgeBase))
	s.mux.HandleFunc("GET /api/v1/knowledge/bases/{id}/docs", s.auth(s.handleListKnowledgeDocs))
	s.mux.HandleFunc("GET /api/v1/knowledge/bases/{id}/graph", s.auth(s.handleKnowledgeGraph))
	s.mux.HandleFunc("GET /api/v1/knowledge/bases/{id}/doc", s.auth(s.handleReadKnowledgeDoc))
	s.mux.HandleFunc("PUT /api/v1/knowledge/bases/{id}/doc", s.auth(s.handleSaveKnowledgeDoc))
	s.mux.HandleFunc("GET /api/v1/knowledge/search", s.auth(s.handleSearchKnowledge))
}

// --- Connection Handlers ---

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListConnections(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": list})
}

func (s *Server) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	var in core.CreateConnectionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	conn, err := s.svc.CreateConnection(r.Context(), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	var in core.UpdateConnectionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	conn, err := s.svc.UpdateConnection(r.Context(), r.PathValue("id"), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, conn)
}

func (s *Server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteConnection(r.Context(), r.PathValue("id")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleTestConnectionDraft(w http.ResponseWriter, r *http.Request) {
	var in core.CreateConnectionInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := s.svc.TestConnectionDraft(r.Context(), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.TestConnection(r.Context(), r.PathValue("id"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// --- GitLab / Jira Query Handlers ---

func (s *Server) handleGitLabProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.svc.GitLabProjects(r.Context(), r.PathValue("id"), r.URL.Query().Get("q"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (s *Server) handleGitLabMRs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mrs, err := s.svc.GitLabMergeRequests(
		r.Context(),
		r.PathValue("id"),
		q.Get("project"),
		q.Get("scope"),
		q.Get("state"),
		q.Get("q"),
	)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"merge_requests": mrs})
}

func (s *Server) handleGitLabCommits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	commits, err := s.svc.GitLabRemoteCommits(r.Context(), r.PathValue("id"), q.Get("project"), q.Get("branch"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": commits})
}

func (s *Server) handleJiraIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var statuses []string
	if raw := strings.TrimSpace(q.Get("statuses")); raw != "" {
		for _, part := range strings.Split(strings.ReplaceAll(raw, "，", ","), ",") {
			if t := strings.TrimSpace(part); t != "" {
				statuses = append(statuses, t)
			}
		}
	}
	issues, err := s.svc.JiraSearchIssues(r.Context(), r.PathValue("id"), components.IssueQuery{
		ProjectKey: q.Get("project"),
		Statuses:   statuses,
		Assignee:   q.Get("assignee"),
		Keyword:    q.Get("q"),
	})
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues})
}

// --- Followups & Task Remote Links ---

func (s *Server) handleListFollowups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.svc.ListIssueFollowups(r.Context(), q.Get("connection_id"), q.Get("local_state"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"followups": list})
}

func (s *Server) handleSetFollowup(w http.ResponseWriter, r *http.Request) {
	var in core.SetFollowupInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	f, err := s.svc.SetIssueFollowup(r.Context(), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleLinkRemote(w http.ResponseWriter, r *http.Request) {
	var in core.CreateRemoteLinkInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	link, err := s.svc.LinkRemote(r.Context(), r.PathValue("id"), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleUnlinkRemote(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.UnlinkRemote(r.Context(), r.PathValue("id"), r.PathValue("lid")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// --- Knowledge Base Handlers ---

func (s *Server) handleListKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.ListKnowledgeBases(r.Context())
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"knowledge_bases": list})
}

func (s *Server) handleCreateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	var in core.CreateKnowledgeBaseInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	kb, err := s.svc.CreateKnowledgeBase(r.Context(), in)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, kb)
}

func (s *Server) handleDeleteKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteKnowledgeBase(r.Context(), r.PathValue("id")); err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleSyncKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	kb, err := s.svc.SyncKnowledgeBase(r.Context(), r.PathValue("id"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, kb)
}

func (s *Server) handleListKnowledgeDocs(w http.ResponseWriter, r *http.Request) {
	docs, err := s.svc.ListKnowledgeDocs(r.Context(), r.PathValue("id"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": docs})
}

func (s *Server) handleKnowledgeGraph(w http.ResponseWriter, r *http.Request) {
	graph, err := s.svc.KnowledgeGraph(r.Context(), r.PathValue("id"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

func (s *Server) handleReadKnowledgeDoc(w http.ResponseWriter, r *http.Request) {
	doc, err := s.svc.ReadKnowledgeDoc(r.Context(), r.PathValue("id"), r.URL.Query().Get("path"))
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleSaveKnowledgeDoc(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	doc, err := s.svc.SaveKnowledgeDoc(r.Context(), r.PathValue("id"), body.Path, body.Content)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleSearchKnowledge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 30
	if raw := q.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	hits, err := s.svc.SearchKnowledge(r.Context(), q.Get("q"), q.Get("kb_id"), limit)
	if err != nil {
		handleServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}
