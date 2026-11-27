// Package core implements Lagrangian's domain use cases and orchestrates
// configuration, persistence, Git reconciliation, and component registries.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lagrangian-dev/lagrangian/internal/components"
	"github.com/lagrangian-dev/lagrangian/internal/config"
	"github.com/lagrangian-dev/lagrangian/internal/domain"
	"github.com/lagrangian-dev/lagrangian/internal/platform/paths"
	"github.com/lagrangian-dev/lagrangian/internal/platform/secrets"
	"github.com/lagrangian-dev/lagrangian/internal/storage"
)

var (
	ErrValidation = errors.New("validation error")
	ErrNotSetup   = errors.New("instance is not initialized")
)

const apiTokenSecretRef = "server.api_token"

type Service struct {
	mu          sync.RWMutex
	layout      paths.Layout
	configPath  string
	cfg         config.Config
	initialized bool
	store       *storage.Store
	secretStore secrets.Store
	registry    *components.Registry
	cachedToken string
}

func NewService(layout paths.Layout, configPath string, secretStore secrets.Store) (*Service, error) {
	cfg, exists, err := config.Load(configPath, layout)
	if err != nil {
		return nil, err
	}

	reg := components.NewRegistry()
	reg.SyncFromConfig(cfg)

	svc := &Service{
		layout:      layout,
		configPath:  configPath,
		cfg:         cfg,
		initialized: exists,
		secretStore: secretStore,
		registry:    reg,
	}

	if exists {
		if err := svc.openStorageLocked(); err != nil {
			return nil, err
		}
		svc.loadAPITokenLocked()
	}
	return svc, nil
}

func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

func (s *Service) openStorageLocked() error {
	if s.store != nil {
		_ = s.store.Close()
		s.store = nil
	}
	st, err := storage.Open(s.cfg.Storage.DBPath, s.cfg.Storage.AttachmentsDir)
	if err != nil {
		return err
	}
	s.store = st
	return nil
}

func (s *Service) loadAPITokenLocked() {
	s.cachedToken = ""
	if !s.cfg.Server.AuthEnabled || s.cfg.Server.APITokenRef == "" || s.secretStore == nil {
		return
	}
	if tok, err := s.secretStore.Get(s.cfg.Server.APITokenRef); err == nil {
		s.cachedToken = tok
	}
}

// --- Setup & Config ---

type SetupStatus struct {
	Initialized   bool          `json:"initialized"`
	ConfigPath    string        `json:"config_path"`
	HostOS        string        `json:"host_os"`
	HostArch      string        `json:"host_arch"`
	SecretBackend string        `json:"secret_backend"`
	DefaultConfig config.Config `json:"default_config"`
}

func (s *Service) SetupStatus() SetupStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	backend := "none"
	if s.secretStore != nil {
		backend = s.secretStore.BackendName()
	}
	return SetupStatus{
		Initialized:   s.initialized,
		ConfigPath:    s.configPath,
		HostOS:        runtime.GOOS,
		HostArch:      runtime.GOARCH,
		SecretBackend: backend,
		DefaultConfig: s.cfg,
	}
}

type SetupInitInput struct {
	Mode          string   `json:"mode"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	DataDir       string   `json:"data_dir"`
	AuthEnabled   bool     `json:"auth_enabled"`
	APIToken      string   `json:"api_token"`
	AuthorAliases []string `json:"author_aliases"`
}

type SetupInitResult struct {
	Config         config.Config `json:"config"`
	GeneratedToken string        `json:"generated_token,omitempty"`
}

func (s *Service) InitSetup(input SetupInitInput) (SetupInitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := config.Default(s.layout)
	if input.Mode != "" {
		cfg.Server.Mode = input.Mode
	}
	if strings.TrimSpace(input.Host) != "" {
		cfg.Server.Host = strings.TrimSpace(input.Host)
	}
	if input.Port > 0 {
		cfg.Server.Port = input.Port
	}
	if strings.TrimSpace(input.DataDir) != "" {
		cfg.Storage.DataDir = strings.TrimSpace(input.DataDir)
		cfg.Storage.DBPath = filepath.Join(cfg.Storage.DataDir, "lagrangian.sqlite")
		cfg.Storage.AttachmentsDir = filepath.Join(cfg.Storage.DataDir, "attachments")
	}
	cfg.Server.AuthEnabled = input.AuthEnabled
	cfg.Git.AuthorAliases = cleanStrings(input.AuthorAliases)

	var generatedToken string
	if cfg.Server.AuthEnabled {
		tok := strings.TrimSpace(input.APIToken)
		if tok == "" {
			tok = randomToken()
			generatedToken = tok
		}
		if s.secretStore == nil {
			return SetupInitResult{}, errors.New("secret store is not available")
		}
		if err := s.secretStore.Set(apiTokenSecretRef, tok); err != nil {
			return SetupInitResult{}, fmt.Errorf("persist api token: %w", err)
		}
		cfg.Server.APITokenRef = apiTokenSecretRef
		s.cachedToken = tok
	} else {
		cfg.Server.APITokenRef = ""
		s.cachedToken = ""
	}

	if err := config.Save(s.configPath, cfg, s.layout); err != nil {
		return SetupInitResult{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	s.cfg = cfg
	s.initialized = true
	if err := s.openStorageLocked(); err != nil {
		return SetupInitResult{}, err
	}
	s.registry.SyncFromConfig(cfg)

	return SetupInitResult{Config: cfg, GeneratedToken: generatedToken}, nil
}

func (s *Service) IsInitialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initialized
}

func (s *Service) Config() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Service) UpdateConfig(next config.Config) (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveConfigLocked(next)
}

type SystemConfigPatch struct {
	Server   *config.ServerConfig   `json:"server,omitempty"`
	Storage  *config.StorageConfig  `json:"storage,omitempty"`
	Workflow *config.WorkflowConfig `json:"workflow,omitempty"`
}

// UpdateSystemConfig updates only base system settings (server/storage/workflow)
// without touching any pluggable component configurations.
func (s *Service) UpdateSystemConfig(patch SystemConfigPatch) (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.cfg
	if patch.Server != nil {
		srv := *patch.Server
		// Preserve secret reference managed by credential store.
		srv.APITokenRef = s.cfg.Server.APITokenRef
		// Clear host so EffectiveHost() derives cleanly from mode (standalone=127.0.0.1, server=0.0.0.0).
		if srv.Mode != s.cfg.Server.Mode {
			srv.Host = ""
		}
		next.Server = srv
	}
	if patch.Storage != nil {
		st := *patch.Storage
		if st.DataDir != "" && st.DataDir != s.cfg.Storage.DataDir {
			st.DBPath = filepath.Join(st.DataDir, "lagrangian.sqlite")
			st.AttachmentsDir = filepath.Join(st.DataDir, "attachments")
		}
		next.Storage = st
	}
	if patch.Workflow != nil {
		next.Workflow = *patch.Workflow
	}
	return s.saveConfigLocked(next)
}

type ComponentConfigInput struct {
	Git   *config.GitConfig      `json:"git,omitempty"`
	Slots []config.ComponentSlot `json:"slots,omitempty"`
	MCP   *config.MCPConfig      `json:"mcp,omitempty"`
}

// UpdateComponentConfig updates a single component family independently.
func (s *Service) UpdateComponentConfig(kind components.Kind, in ComponentConfigInput) (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.cfg
	switch kind {
	case components.KindCodeHost:
		if in.Git != nil {
			next.Git = config.GitConfig{
				AuthorAliases:       cleanStrings(in.Git.AuthorAliases),
				AutoReconcileRebase: in.Git.AutoReconcileRebase,
			}
		}
		if in.Slots != nil {
			next.Components.CodeHosts = in.Slots
		}
	case components.KindIssueTracker:
		if in.Slots != nil {
			next.Components.IssueTrackers = in.Slots
		}
	case components.KindKnowledgeBase:
		if in.Slots != nil {
			next.Components.KnowledgeBases = in.Slots
		}
	case components.KindAgentSource:
		if in.Slots != nil {
			next.Components.AgentSources = in.Slots
		}
	case components.KindLLMProvider:
		if in.Slots != nil {
			next.Components.LLMProviders = in.Slots
		}
	case components.KindBotChannel:
		if in.Slots != nil {
			next.Components.Bots = in.Slots
		}
	case components.KindMCPGateway:
		if in.MCP != nil {
			next.Components.MCP = *in.MCP
		}
	default:
		return config.Config{}, fmt.Errorf("%w: 未知组件类型 %q", ErrValidation, kind)
	}
	return s.saveConfigLocked(next)
}

func (s *Service) saveConfigLocked(next config.Config) (config.Config, error) {
	prevDB := s.cfg.Storage.DBPath
	prevAtt := s.cfg.Storage.AttachmentsDir
	next.Normalize(s.layout)
	if err := config.Save(s.configPath, next, s.layout); err != nil {
		return config.Config{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	s.cfg = next
	s.initialized = true
	if s.store == nil || prevDB != next.Storage.DBPath || prevAtt != next.Storage.AttachmentsDir {
		if err := s.openStorageLocked(); err != nil {
			return config.Config{}, err
		}
	}
	s.loadAPITokenLocked()
	s.registry.SyncFromConfig(next)
	return s.cfg, nil
}

func (s *Service) VerifyToken(candidate string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.initialized || !s.cfg.Server.AuthEnabled {
		return true
	}
	if s.cachedToken == "" {
		return false
	}
	return candidate == s.cachedToken
}

func (s *Service) ComponentStatuses(ctx context.Context) []components.Status {
	return s.livePhase2Statuses(ctx)
}

func (s *Service) ListWorkstations(ctx context.Context) ([]domain.WorkstationNode, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListWorkstations(ctx)
}

func (s *Service) requireStore() (*storage.Store, config.Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.initialized || s.store == nil {
		return nil, s.cfg, ErrNotSetup
	}
	return s.store, s.cfg, nil
}

// --- Tasks ---

type CreateTaskInput struct {
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Status          string   `json:"status"`
	Priority        string   `json:"priority"`
	NextAction      string   `json:"next_action"`
	Blocker         string   `json:"blocker"`
	ResumeCondition string   `json:"resume_condition"`
	Tags            []string `json:"tags"`
	DueDate         string   `json:"due_date"`
}

func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (domain.Task, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return domain.Task{}, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.Task{}, fmt.Errorf("%w: 任务标题不能为空", ErrValidation)
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = cfg.Workflow.TaskStatuses[0].Key
	}
	if !cfg.HasStatus(status) {
		return domain.Task{}, fmt.Errorf("%w: 未知任务状态 %q", ErrValidation, status)
	}
	priority := normalizePriority(in.Priority)
	now := time.Now().UTC()
	var completedAt *time.Time
	if cfg.StatusCategory(status) == "done" {
		completedAt = &now
	}

	task := domain.Task{
		ID:              uuid.NewString(),
		Title:           title,
		Description:     strings.TrimSpace(in.Description),
		Status:          status,
		Priority:        priority,
		NextAction:      strings.TrimSpace(in.NextAction),
		Blocker:         strings.TrimSpace(in.Blocker),
		ResumeCondition: strings.TrimSpace(in.ResumeCondition),
		Tags:            cleanStrings(in.Tags),
		DueDate:         strings.TrimSpace(in.DueDate),
		Archived:        false,
		CreatedAt:       now,
		UpdatedAt:       now,
		CompletedAt:     completedAt,
	}
	return store.CreateTask(ctx, task)
}

type UpdateTaskInput struct {
	Title           *string   `json:"title"`
	Description     *string   `json:"description"`
	Status          *string   `json:"status"`
	Priority        *string   `json:"priority"`
	NextAction      *string   `json:"next_action"`
	Blocker         *string   `json:"blocker"`
	ResumeCondition *string   `json:"resume_condition"`
	Tags            *[]string `json:"tags"`
	DueDate         *string   `json:"due_date"`
	Archived        *bool     `json:"archived"`
}

func (s *Service) UpdateTask(ctx context.Context, id string, in UpdateTaskInput) (domain.Task, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return domain.Task{}, err
	}
	task, err := store.GetTask(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return domain.Task{}, fmt.Errorf("%w: 任务标题不能为空", ErrValidation)
		}
		task.Title = t
	}
	if in.Description != nil {
		task.Description = strings.TrimSpace(*in.Description)
	}
	if in.Status != nil {
		st := strings.TrimSpace(*in.Status)
		if !cfg.HasStatus(st) {
			return domain.Task{}, fmt.Errorf("%w: 未知任务状态 %q", ErrValidation, st)
		}
		task.Status = st
		now := time.Now().UTC()
		if cfg.StatusCategory(st) == "done" {
			if task.CompletedAt == nil {
				task.CompletedAt = &now
			}
		} else {
			task.CompletedAt = nil
		}
	}
	if in.Priority != nil {
		task.Priority = normalizePriority(*in.Priority)
	}
	if in.NextAction != nil {
		task.NextAction = strings.TrimSpace(*in.NextAction)
	}
	if in.Blocker != nil {
		task.Blocker = strings.TrimSpace(*in.Blocker)
	}
	if in.ResumeCondition != nil {
		task.ResumeCondition = strings.TrimSpace(*in.ResumeCondition)
	}
	if in.Tags != nil {
		task.Tags = cleanStrings(*in.Tags)
	}
	if in.DueDate != nil {
		task.DueDate = strings.TrimSpace(*in.DueDate)
	}
	if in.Archived != nil {
		task.Archived = *in.Archived
	}
	task.UpdatedAt = time.Now().UTC()
	return store.UpdateTask(ctx, task)
}

func (s *Service) DeleteTask(ctx context.Context, id string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	return store.DeleteTask(ctx, id)
}

func (s *Service) ListTasks(ctx context.Context, f storage.TaskFilter) ([]domain.Task, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListTasks(ctx, f)
}

func (s *Service) GetTaskDetail(ctx context.Context, id string) (domain.TaskDetail, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.TaskDetail{}, err
	}
	task, err := store.GetTask(ctx, id)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	progress, err := store.ListProgress(ctx, id)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	attachments, err := store.ListAttachments(ctx, id)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	byProgress := map[string][]domain.Attachment{}
	for _, a := range attachments {
		if a.ProgressID != "" {
			byProgress[a.ProgressID] = append(byProgress[a.ProgressID], a)
		}
	}
	for i := range progress {
		progress[i].Attachments = byProgress[progress[i].ID]
	}

	repos, err := store.ListRepoBindings(ctx, id)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	commits, err := store.ListCommitLinks(ctx, id, false)
	if err != nil {
		return domain.TaskDetail{}, err
	}
	remoteLinks, err := store.ListRemoteLinks(ctx, id)
	if err != nil {
		return domain.TaskDetail{}, err
	}

	return domain.TaskDetail{
		Task:        task,
		Progress:    progress,
		Attachments: attachments,
		Repos:       repos,
		Commits:     commits,
		RemoteLinks: remoteLinks,
	}, nil
}

// --- Progress & Attachments ---

type AddProgressInput struct {
	Title      string `json:"title"`
	Body       string `json:"body"`
	OccurredOn string `json:"occurred_on"`
	Source     string `json:"source"`
}

func (s *Service) AddProgress(ctx context.Context, taskID string, in AddProgressInput) (domain.ProgressEntry, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.ProgressEntry{}, err
	}
	if _, err := store.GetTask(ctx, taskID); err != nil {
		return domain.ProgressEntry{}, err
	}
	body := strings.TrimSpace(in.Body)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		if body == "" {
			return domain.ProgressEntry{}, fmt.Errorf("%w: 进展标题或正文不能同时为空", ErrValidation)
		}
		firstLine := strings.SplitN(body, "\n", 2)[0]
		title = truncateRunes(firstLine, 60)
	}
	occurredOn := strings.TrimSpace(in.OccurredOn)
	if occurredOn == "" {
		occurredOn = time.Now().Format("2006-01-02")
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = "manual"
	}
	now := time.Now().UTC()
	return store.CreateProgress(ctx, domain.ProgressEntry{
		ID:         uuid.NewString(),
		TaskID:     taskID,
		Title:      title,
		Body:       body,
		OccurredOn: occurredOn,
		Source:     source,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

func (s *Service) DeleteProgress(ctx context.Context, taskID, progressID string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	return store.DeleteProgress(ctx, taskID, progressID)
}

func (s *Service) UploadAttachment(ctx context.Context, taskID, progressID, filename, mimeType string, content io.Reader) (domain.Attachment, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.Attachment{}, err
	}
	if _, err := store.GetTask(ctx, taskID); err != nil {
		return domain.Attachment{}, err
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	return store.SaveAttachment(ctx, domain.Attachment{
		ID:         uuid.NewString(),
		TaskID:     taskID,
		ProgressID: strings.TrimSpace(progressID),
		Filename:   filename,
		MimeType:   mimeType,
		CreatedAt:  time.Now().UTC(),
	}, content)
}

func (s *Service) GetAttachment(ctx context.Context, attachmentID string) (domain.Attachment, string, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.Attachment{}, "", err
	}
	return store.GetAttachment(ctx, attachmentID)
}

// --- Repo Bindings & Commits ---

type BindRepoInput struct {
	WorkstationID string `json:"workstation_id"`
	RepoName      string `json:"repo_name"`
	RepoPath      string `json:"repo_path"`
	BranchName    string `json:"branch_name"`
	RemoteURL     string `json:"remote_url"`
}

func (s *Service) BindRepo(ctx context.Context, taskID string, in BindRepoInput) (domain.RepoBinding, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.RepoBinding{}, err
	}
	if _, err := store.GetTask(ctx, taskID); err != nil {
		return domain.RepoBinding{}, err
	}
	repoPath := strings.TrimSpace(in.RepoPath)
	repoName := strings.TrimSpace(in.RepoName)
	if repoName == "" && repoPath != "" {
		repoName = filepath.Base(repoPath)
	}
	branch := strings.TrimSpace(in.BranchName)
	if repoName == "" || branch == "" {
		return domain.RepoBinding{}, fmt.Errorf("%w: 仓库名称与分支名不能为空", ErrValidation)
	}
	return store.CreateRepoBinding(ctx, domain.RepoBinding{
		ID:            uuid.NewString(),
		TaskID:        taskID,
		WorkstationID: strings.TrimSpace(in.WorkstationID),
		RepoName:      repoName,
		RepoPath:      repoPath,
		BranchName:    branch,
		RemoteURL:     strings.TrimSpace(in.RemoteURL),
		CreatedAt:     time.Now().UTC(),
	})
}

func (s *Service) UnbindRepo(ctx context.Context, taskID, bindingID string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	return store.DeleteRepoBinding(ctx, taskID, bindingID)
}

func (s *Service) CommitCandidates(ctx context.Context, taskID string, limit int) ([]domain.CommitCandidate, []string, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return nil, nil, err
	}
	repos, err := store.ListRepoBindings(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	candidates := make([]domain.CommitCandidate, 0)
	warnings := make([]string, 0)
	seen := map[string]bool{}

	for _, r := range repos {
		if r.RepoPath == "" {
			warnings = append(warnings, fmt.Sprintf("%s/%s 未配置本地仓库路径（远端 Git 托管候选将在 Phase 2 接入）", r.RepoName, r.BranchName))
			continue
		}
		if _, statErr := os.Stat(r.RepoPath); statErr != nil {
			warnings = append(warnings, fmt.Sprintf("当前节点无法访问路径 %s（若位于远端工作节点，将在 Phase 3 由 lagrangian-agent 同步）", r.RepoPath))
			continue
		}
		commits, err := InspectBranchCommits(ctx, r.RepoPath, r.BranchName, cfg.Git.AuthorAliases, limit)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		for _, c := range commits {
			key := r.RepoName + ":" + c.SHA
			if seen[key] {
				continue
			}
			seen[key] = true
			cand := domain.CommitCandidate{
				RepoName:    r.RepoName,
				RepoPath:    r.RepoPath,
				BranchName:  r.BranchName,
				SHA:         c.SHA,
				Title:       c.Title,
				Author:      c.Author,
				CommittedAt: c.CommittedAt,
				PatchID:     c.PatchID,
			}
			if existing, err := store.GetCommitLinkByRepoSHA(ctx, r.RepoName, c.SHA); err == nil {
				cand.AlreadyBound = true
				cand.BoundTaskID = existing.TaskID
				cand.Superseded = existing.SupersededBy != ""
			}
			candidates = append(candidates, cand)
		}
	}
	return candidates, warnings, nil
}

type BindCommitInput struct {
	RepoName    string `json:"repo_name"`
	BranchName  string `json:"branch_name"`
	SHA         string `json:"sha"`
	Title       string `json:"title"`
	Author      string `json:"author"`
	CommittedAt string `json:"committed_at"`
	PatchID     string `json:"patch_id"`
	Source      string `json:"source"`
}

func (s *Service) BindCommits(ctx context.Context, taskID string, items []BindCommitInput) ([]domain.CommitLink, []string, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, nil, err
	}
	if _, err := store.GetTask(ctx, taskID); err != nil {
		return nil, nil, err
	}
	bound := make([]domain.CommitLink, 0, len(items))
	skipped := make([]string, 0)
	now := time.Now().UTC()

	for _, item := range items {
		repo := strings.TrimSpace(item.RepoName)
		sha := strings.TrimSpace(item.SHA)
		if repo == "" || sha == "" {
			skipped = append(skipped, "缺少仓库名或 SHA")
			continue
		}
		if existing, err := store.GetCommitLinkByRepoSHA(ctx, repo, sha); err == nil {
			if existing.SupersededBy != "" {
				skipped = append(skipped, fmt.Sprintf("%s 已被 rebase 替代为 %s，跳过绑定", shortSHA(sha), shortSHA(existing.SupersededBy)))
			} else {
				skipped = append(skipped, fmt.Sprintf("%s 已绑定，跳过重复关联", shortSHA(sha)))
			}
			continue
		}
		src := strings.TrimSpace(item.Source)
		if src == "" {
			src = "manual"
		}
		link, err := store.CreateCommitLink(ctx, domain.CommitLink{
			ID:          uuid.NewString(),
			TaskID:      taskID,
			RepoName:    repo,
			BranchName:  strings.TrimSpace(item.BranchName),
			SHA:         sha,
			Title:       strings.TrimSpace(item.Title),
			Author:      strings.TrimSpace(item.Author),
			CommittedAt: strings.TrimSpace(item.CommittedAt),
			PatchID:     strings.TrimSpace(item.PatchID),
			Source:      src,
			CreatedAt:   now,
		})
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s 绑定失败: %v", shortSHA(sha), err))
			continue
		}
		bound = append(bound, link)
	}
	return bound, skipped, nil
}

func (s *Service) UnbindCommit(ctx context.Context, taskID, commitID string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	return store.DeleteCommitLink(ctx, taskID, commitID)
}

// ReconcileTaskRebases checks active commits bound to taskID against current
// commits on the task's local bound branches. Only strict 1-to-1 patch-id matches
// are superseded automatically; same-title commits with distinct patches are preserved.
func (s *Service) ReconcileTaskRebases(ctx context.Context, taskID string) (domain.ReconcileResult, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return domain.ReconcileResult{}, err
	}
	repos, err := store.ListRepoBindings(ctx, taskID)
	if err != nil {
		return domain.ReconcileResult{}, err
	}
	activeLinks, err := store.ListCommitLinks(ctx, taskID, false)
	if err != nil {
		return domain.ReconcileResult{}, err
	}

	res := domain.ReconcileResult{
		Replaced: []domain.ReconcileReplacement{},
		Warnings: []string{},
	}

	for _, repo := range repos {
		if repo.RepoPath == "" {
			continue
		}
		if _, statErr := os.Stat(repo.RepoPath); statErr != nil {
			continue
		}
		branchCommits, err := InspectBranchCommits(ctx, repo.RepoPath, repo.BranchName, cfg.Git.AuthorAliases, 200)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		res.ScannedBranches++

		liveBySHA := map[string]GitCommit{}
		liveByPatch := map[string][]GitCommit{}
		for _, c := range branchCommits {
			liveBySHA[c.SHA] = c
			if c.PatchID != "" {
				liveByPatch[c.PatchID] = append(liveByPatch[c.PatchID], c)
			}
		}

		missingByPatch := map[string][]domain.CommitLink{}
		for _, link := range activeLinks {
			if link.RepoName != repo.RepoName || link.PatchID == "" {
				continue
			}
			if _, stillPresent := liveBySHA[link.SHA]; stillPresent {
				continue
			}
			missingByPatch[link.PatchID] = append(missingByPatch[link.PatchID], link)
		}

		for patchID, missing := range missingByPatch {
			if len(missing) != 1 {
				continue
			}
			oldLink := missing[0]
			var newMatches []GitCommit
			for _, cand := range liveByPatch[patchID] {
				if cand.SHA == oldLink.SHA {
					continue
				}
				if _, err := store.GetCommitLinkByRepoSHA(ctx, repo.RepoName, cand.SHA); err == nil {
					continue
				}
				newMatches = append(newMatches, cand)
			}
			if len(newMatches) != 1 {
				continue
			}
			replacement := newMatches[0]
			newLink := domain.CommitLink{
				ID:          uuid.NewString(),
				TaskID:      taskID,
				RepoName:    repo.RepoName,
				BranchName:  repo.BranchName,
				SHA:         replacement.SHA,
				Title:       replacement.Title,
				Author:      replacement.Author,
				CommittedAt: replacement.CommittedAt,
				PatchID:     replacement.PatchID,
				Source:      "rebase",
				CreatedAt:   time.Now().UTC(),
			}
			if err := store.SupersedeCommit(ctx, oldLink.ID, newLink); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("归并 %s 失败: %v", shortSHA(oldLink.SHA), err))
				continue
			}
			res.Replaced = append(res.Replaced, domain.ReconcileReplacement{
				RepoName:   repo.RepoName,
				BranchName: repo.BranchName,
				OldSHA:     oldLink.SHA,
				NewSHA:     replacement.SHA,
				Title:      replacement.Title,
				PatchID:    patchID,
			})
		}
	}

	return res, nil
}

// --- Orbit Visualization Snapshot ---

func (s *Service) OrbitSnapshot(ctx context.Context) (domain.OrbitSnapshot, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return domain.OrbitSnapshot{}, err
	}
	tasks, err := store.ListTasks(ctx, storage.TaskFilter{IncludeArchived: false})
	if err != nil {
		return domain.OrbitSnapshot{}, err
	}
	repos, err := store.ListAllRepoBindings(ctx)
	if err != nil {
		return domain.OrbitSnapshot{}, err
	}
	activeCommits, err := store.CountActiveCommits(ctx)
	if err != nil {
		return domain.OrbitSnapshot{}, err
	}
	workstations, err := store.ListWorkstations(ctx)
	if err != nil {
		return domain.OrbitSnapshot{}, err
	}
	remoteLinks, err := store.ListAllRemoteLinks(ctx)
	if err != nil {
		return domain.OrbitSnapshot{}, err
	}
	compStatuses := s.livePhase2Statuses(ctx)

	statusColor := map[string]string{}
	statusLabel := map[string]string{}
	for _, st := range cfg.Workflow.TaskStatuses {
		statusColor[st.Key] = st.Color
		statusLabel[st.Key] = st.Label
	}

	nodes := make([]domain.OrbitNode, 0, len(tasks)+len(repos)+len(compStatuses))
	stats := domain.OrbitStats{
		TotalTasks:            len(tasks),
		BoundRepos:            len(repos),
		ActiveCommits:         activeCommits,
		ConnectedWorkstations: len(workstations),
		TotalComponents:       len(compStatuses),
	}

	// Ring 1: Active / Blocked / Open Tasks
	for _, t := range tasks {
		cat := cfg.StatusCategory(t.Status)
		if cat == "active" {
			stats.ActiveTasks++
		}
		if cat == "blocked" {
			stats.BlockedTasks++
		}
		if cat == "done" {
			continue
		}
		color := statusColor[t.Status]
		if color == "" {
			color = "#58a6ff"
		}
		subtitle := statusLabel[t.Status]
		if t.NextAction != "" {
			subtitle += " · " + truncateRunes(t.NextAction, 28)
		}
		nodes = append(nodes, domain.OrbitNode{
			ID:       t.ID,
			Ring:     1,
			Kind:     "task",
			Label:    truncateRunes(t.Title, 24),
			Subtitle: subtitle,
			Status:   t.Status,
			Color:    color,
			Meta: map[string]string{
				"task_id":  t.ID,
				"priority": t.Priority,
				"blocker":  t.Blocker,
			},
		})
	}

	// Ring 2: Bound Repositories & Branches + Linked Remote Issues/MRs
	seenRepo := map[string]bool{}
	for _, r := range repos {
		key := r.RepoName + "@" + r.BranchName
		if seenRepo[key] {
			continue
		}
		seenRepo[key] = true
		nodes = append(nodes, domain.OrbitNode{
			ID:       r.ID,
			Ring:     2,
			Kind:     "repo",
			Label:    r.RepoName,
			Subtitle: r.BranchName,
			Status:   "bound",
			Color:    "#38bdf8",
			Meta: map[string]string{
				"task_id":        r.TaskID,
				"workstation_id": r.WorkstationID,
				"repo_path":      r.RepoPath,
				"branch":         r.BranchName,
			},
		})
	}
	seenRemote := map[string]bool{}
	for _, rl := range remoteLinks {
		key := rl.RemoteKind + ":" + rl.RemoteKey
		if seenRemote[key] {
			continue
		}
		seenRemote[key] = true
		color := "#f59e0b"
		if rl.RemoteKind == "merge_request" {
			color = "#c084fc"
		}
		nodes = append(nodes, domain.OrbitNode{
			ID:       rl.ID,
			Ring:     2,
			Kind:     "repo",
			Label:    truncateRunes(rl.RemoteKey, 20),
			Subtitle: truncateRunes(rl.Title, 32),
			Status:   rl.RemoteState,
			Color:    color,
			Meta: map[string]string{
				"task_id":     rl.TaskID,
				"remote_kind": rl.RemoteKind,
				"web_url":     rl.WebURL,
			},
		})
	}

	// Ring 3: Pluggable Components
	for _, c := range compStatuses {
		if c.Enabled {
			stats.EnabledComponents++
		}
		color := "#475569"
		switch c.State {
		case components.HealthReady:
			color = "#34d399"
		case components.HealthPlanned:
			color = "#a78bfa"
		case components.HealthDegraded:
			color = "#fb923c"
		}
		nodes = append(nodes, domain.OrbitNode{
			ID:       c.ID,
			Ring:     3,
			Kind:     "component",
			Label:    c.Name,
			Subtitle: c.Detail,
			Status:   string(c.State),
			Color:    color,
			Meta: map[string]string{
				"kind":   string(c.Kind),
				"driver": c.Driver,
			},
		})
	}

	return domain.OrbitSnapshot{
		CenterLabel: "Lagrangian Core",
		Mode:        cfg.Server.Mode,
		HostOS:      runtime.GOOS + "/" + runtime.GOARCH,
		Nodes:       nodes,
		Stats:       stats,
	}, nil
}

// --- Helpers ---

func normalizePriority(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "low", "medium", "high", "urgent":
		return strings.ToLower(strings.TrimSpace(p))
	default:
		return "medium"
	}
}

func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func truncateRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max-1]) + "…"
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "lg_" + hex.EncodeToString(b)
}
