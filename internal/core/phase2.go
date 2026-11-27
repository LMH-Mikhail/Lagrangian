package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lagrangian-dev/lagrangian/internal/components"
	"github.com/lagrangian-dev/lagrangian/internal/components/codehost/github"
	"github.com/lagrangian-dev/lagrangian/internal/components/codehost/gitlab"
	"github.com/lagrangian-dev/lagrangian/internal/components/issuetracker/jira"
	"github.com/lagrangian-dev/lagrangian/internal/components/knowledge"
	"github.com/lagrangian-dev/lagrangian/internal/domain"
	"github.com/lagrangian-dev/lagrangian/internal/platform/secrets"
	"github.com/lagrangian-dev/lagrangian/internal/storage"
)

// --- External Integration Connections (GitLab / Jira) ---

type CreateConnectionInput struct {
	ConnectionID string            `json:"connection_id,omitempty"` // optional: used by TestConnectionDraft when editing an existing connection
	Kind         string            `json:"kind"`                    // "code_host" | "issue_tracker"
	Driver       string            `json:"driver"`                  // "gitlab" | "jira"
	Name         string            `json:"name"`
	BaseURL      string            `json:"base_url"`
	AuthType     string            `json:"auth_type"` // "pat" | "basic" | "oauth_password"
	Username     string            `json:"username"`
	Secret       string            `json:"secret"`
	Options      map[string]string `json:"options"`
	Enabled      *bool             `json:"enabled"`
}

type UpdateConnectionInput struct {
	Name     *string            `json:"name"`
	BaseURL  *string            `json:"base_url"`
	AuthType *string            `json:"auth_type"`
	Username *string            `json:"username"`
	Secret   *string            `json:"secret"`
	Options  *map[string]string `json:"options"`
	Enabled  *bool              `json:"enabled"`
}

func (s *Service) ListConnections(ctx context.Context, kind string) ([]domain.IntegrationConnection, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListConnections(ctx, strings.TrimSpace(kind))
}

func (s *Service) CreateConnection(ctx context.Context, in CreateConnectionInput) (domain.IntegrationConnection, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	kind := strings.TrimSpace(in.Kind)
	driver := strings.ToLower(strings.TrimSpace(in.Driver))
	if kind == "" {
		switch driver {
		case "gitlab", "github":
			kind = "code_host"
		case "jira":
			kind = "issue_tracker"
		}
	}
	validCombo := (kind == "code_host" && (driver == "gitlab" || driver == "github")) ||
		(kind == "issue_tracker" && driver == "jira")
	if !validCombo {
		return domain.IntegrationConnection{}, fmt.Errorf("%w: 当前支持 code_host(gitlab/github) 与 issue_tracker(jira)", ErrValidation)
	}
	name := strings.TrimSpace(in.Name)
	baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if driver == "github" {
		baseURL = github.NormalizeBaseURL(baseURL)
	}
	if name == "" || baseURL == "" {
		return domain.IntegrationConnection{}, fmt.Errorf("%w: 连接名称与服务地址不能为空", ErrValidation)
	}
	authType := strings.ToLower(strings.TrimSpace(in.AuthType))
	if authType == "" {
		authType = "pat"
	}
	if err := validateConnectionAuthType(driver, authType); err != nil {
		return domain.IntegrationConnection{}, err
	}
	username := strings.TrimSpace(in.Username)
	rawSecret := strings.TrimSpace(in.Secret)
	secretToStore := rawSecret

	// Always verify remote connectivity before persisting any connection or secret.
	now := time.Now().UTC()
	switch driver {
	case "gitlab":
		if authType == "oauth_password" {
			if username == "" || rawSecret == "" {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: GitLab OAuth2 密码登录需填写用户名和密码", ErrValidation)
			}
			session, err := gitlab.New(gitlab.Config{BaseURL: baseURL}).ExchangePasswordToken(ctx, username, rawSecret)
			if err != nil {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: %v", ErrValidation, err)
			}
			if _, err := gitlab.New(gitlab.Config{
				BaseURL:  baseURL,
				AuthType: authType,
				Username: username,
				Secret:   session.AccessToken,
			}).TestConnection(ctx); err != nil {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存连接：%v", ErrValidation, err)
			}
			enc, err := json.Marshal(session)
			if err != nil {
				return domain.IntegrationConnection{}, err
			}
			secretToStore = string(enc)
		} else {
			if rawSecret == "" {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: GitLab PAT 不能为空", ErrValidation)
			}
			if _, err := gitlab.New(gitlab.Config{
				BaseURL:  baseURL,
				AuthType: authType,
				Username: username,
				Secret:   rawSecret,
			}).TestConnection(ctx); err != nil {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存连接：%v", ErrValidation, err)
			}
		}
	case "github":
		if rawSecret == "" {
			return domain.IntegrationConnection{}, fmt.Errorf("%w: GitHub PAT 不能为空", ErrValidation)
		}
		if _, err := github.New(github.Config{
			BaseURL:  baseURL,
			AuthType: authType,
			Username: username,
			Secret:   rawSecret,
		}).TestConnection(ctx); err != nil {
			return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存连接：%v", ErrValidation, err)
		}
	case "jira":
		if authType == "basic" && (username == "" || rawSecret == "") {
			return domain.IntegrationConnection{}, fmt.Errorf("%w: Jira Basic 认证需填写用户名和密码/Token", ErrValidation)
		}
		if authType == "pat" && rawSecret == "" {
			return domain.IntegrationConnection{}, fmt.Errorf("%w: Jira PAT 不能为空", ErrValidation)
		}
		if _, err := jira.New(jira.Config{
			BaseURL:  baseURL,
			AuthType: authType,
			Username: username,
			Secret:   rawSecret,
		}).TestConnection(ctx); err != nil {
			return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存连接：%v", ErrValidation, err)
		}
	}

	id := uuid.NewString()
	secretRef := ""
	if secretToStore != "" {
		if s.secretStore == nil {
			return domain.IntegrationConnection{}, errors.New("凭据存储不可用")
		}
		secretRef = "conn." + id + ".secret"
		if err := s.secretStore.Set(secretRef, secretToStore); err != nil {
			return domain.IntegrationConnection{}, fmt.Errorf("保存连接凭据失败: %w", err)
		}
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	conn := domain.IntegrationConnection{
		ID:            id,
		Kind:          kind,
		Driver:        driver,
		Name:          name,
		BaseURL:       baseURL,
		AuthType:      authType,
		Username:      username,
		SecretRef:     secretRef,
		HasSecret:     secretRef != "",
		Options:       in.Options,
		Enabled:       enabled,
		LastCheckedAt: now.Format(time.RFC3339),
		LastError:     "",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	created, err := store.CreateConnection(ctx, conn)
	if err != nil {
		if secretRef != "" && s.secretStore != nil {
			_ = s.secretStore.Delete(secretRef)
		}
		return domain.IntegrationConnection{}, err
	}
	return created, nil
}

func (s *Service) UpdateConnection(ctx context.Context, id string, in UpdateConnectionInput) (domain.IntegrationConnection, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	conn, err := store.GetConnection(ctx, id)
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	origBaseURL := conn.BaseURL
	origAuthType := conn.AuthType
	origUsername := conn.Username

	if in.Name != nil {
		if v := strings.TrimSpace(*in.Name); v != "" {
			conn.Name = v
		}
	}
	if in.BaseURL != nil {
		if v := strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/"); v != "" {
			if conn.Driver == "github" {
				v = github.NormalizeBaseURL(v)
			}
			conn.BaseURL = v
		}
	}
	if in.AuthType != nil {
		v := strings.ToLower(strings.TrimSpace(*in.AuthType))
		if err := validateConnectionAuthType(conn.Driver, v); err != nil {
			return domain.IntegrationConnection{}, err
		}
		conn.AuthType = v
	}
	if in.Username != nil {
		conn.Username = strings.TrimSpace(*in.Username)
	}
	if in.Options != nil {
		conn.Options = *in.Options
	}
	if in.Enabled != nil {
		conn.Enabled = *in.Enabled
	}

	newRawSecret := ""
	if in.Secret != nil {
		newRawSecret = strings.TrimSpace(*in.Secret)
	}
	credChanged := conn.BaseURL != origBaseURL || conn.AuthType != origAuthType || conn.Username != origUsername || newRawSecret != ""
	secretToStore := newRawSecret

	if credChanged {
		nowStr := time.Now().UTC().Format(time.RFC3339)
		switch conn.Driver {
		case "gitlab":
			if conn.AuthType == "oauth_password" {
				if newRawSecret != "" {
					if conn.Username == "" {
						return domain.IntegrationConnection{}, fmt.Errorf("%w: GitLab OAuth2 密码登录需填写用户名", ErrValidation)
					}
					session, err := gitlab.New(gitlab.Config{BaseURL: conn.BaseURL}).ExchangePasswordToken(ctx, conn.Username, newRawSecret)
					if err != nil {
						return domain.IntegrationConnection{}, fmt.Errorf("%w: %v", ErrValidation, err)
					}
					if _, err := gitlab.New(gitlab.Config{
						BaseURL:  conn.BaseURL,
						AuthType: conn.AuthType,
						Username: conn.Username,
						Secret:   session.AccessToken,
					}).TestConnection(ctx); err != nil {
						return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存修改：%v", ErrValidation, err)
					}
					enc, err := json.Marshal(session)
					if err != nil {
						return domain.IntegrationConnection{}, err
					}
					secretToStore = string(enc)
				} else {
					client, err := s.buildGitLabClient(ctx, conn)
					if err != nil {
						return domain.IntegrationConnection{}, fmt.Errorf("%w: %v", ErrValidation, err)
					}
					if _, err := client.TestConnection(ctx); err != nil {
						return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存修改：%v", ErrValidation, err)
					}
				}
			} else {
				effectiveSec := newRawSecret
				if effectiveSec == "" {
					effectiveSec, _ = s.resolveSecret(conn.SecretRef)
				}
				if effectiveSec == "" {
					return domain.IntegrationConnection{}, fmt.Errorf("%w: GitLab PAT 不能为空", ErrValidation)
				}
				if _, err := gitlab.New(gitlab.Config{
					BaseURL:  conn.BaseURL,
					AuthType: conn.AuthType,
					Username: conn.Username,
					Secret:   effectiveSec,
				}).TestConnection(ctx); err != nil {
					return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存修改：%v", ErrValidation, err)
				}
			}
		case "github":
			effectiveSec := newRawSecret
			if effectiveSec == "" {
				effectiveSec, _ = s.resolveSecret(conn.SecretRef)
			}
			if effectiveSec == "" {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: GitHub PAT 不能为空", ErrValidation)
			}
			if _, err := github.New(github.Config{
				BaseURL:  conn.BaseURL,
				AuthType: conn.AuthType,
				Username: conn.Username,
				Secret:   effectiveSec,
			}).TestConnection(ctx); err != nil {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存修改：%v", ErrValidation, err)
			}
		case "jira":
			effectiveSec := newRawSecret
			if effectiveSec == "" {
				effectiveSec, _ = s.resolveSecret(conn.SecretRef)
			}
			if conn.AuthType == "basic" && (conn.Username == "" || effectiveSec == "") {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: Jira Basic 认证需填写用户名和密码/Token", ErrValidation)
			}
			if conn.AuthType == "pat" && effectiveSec == "" {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: Jira PAT 不能为空", ErrValidation)
			}
			if _, err := jira.New(jira.Config{
				BaseURL:  conn.BaseURL,
				AuthType: conn.AuthType,
				Username: conn.Username,
				Secret:   effectiveSec,
			}).TestConnection(ctx); err != nil {
				return domain.IntegrationConnection{}, fmt.Errorf("%w: 连通性校验未通过，未保存修改：%v", ErrValidation, err)
			}
		}
		conn.LastCheckedAt = nowStr
		conn.LastError = ""
	}

	if secretToStore != "" {
		if s.secretStore == nil {
			return domain.IntegrationConnection{}, errors.New("凭据存储不可用")
		}
		if conn.SecretRef == "" {
			conn.SecretRef = "conn." + conn.ID + ".secret"
		}
		if err := s.secretStore.Set(conn.SecretRef, secretToStore); err != nil {
			return domain.IntegrationConnection{}, fmt.Errorf("更新连接凭据失败: %w", err)
		}
		conn.HasSecret = true
	}
	conn.UpdatedAt = time.Now().UTC()
	return store.UpdateConnection(ctx, conn)
}

func (s *Service) DeleteConnection(ctx context.Context, id string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	conn, err := store.GetConnection(ctx, id)
	if err != nil {
		return err
	}
	if err := store.DeleteConnection(ctx, id); err != nil {
		return err
	}
	if conn.SecretRef != "" && s.secretStore != nil {
		_ = s.secretStore.Delete(conn.SecretRef)
	}
	return nil
}

type ConnectionTestResult struct {
	OK        bool     `json:"ok"`
	Identity  string   `json:"identity,omitempty"`
	Error     string   `json:"error,omitempty"`
	CheckedAt string   `json:"checked_at"`
	Statuses  []string `json:"statuses,omitempty"`
}

// TestConnectionDraft verifies remote connectivity for unsaved connection parameters
// without writing anything to SQLite or secret storage. For Jira, it also fetches
// the instance's available localized status names to assist whitelist selection.
func (s *Service) TestConnectionDraft(ctx context.Context, in CreateConnectionInput) (ConnectionTestResult, error) {
	driver := strings.ToLower(strings.TrimSpace(in.Driver))
	baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if driver == "github" {
		baseURL = github.NormalizeBaseURL(baseURL)
	}
	if baseURL == "" {
		return ConnectionTestResult{}, fmt.Errorf("%w: 服务地址不能为空", ErrValidation)
	}
	authType := strings.ToLower(strings.TrimSpace(in.AuthType))
	if authType == "" {
		authType = "pat"
	}
	if err := validateConnectionAuthType(driver, authType); err != nil {
		return ConnectionTestResult{}, err
	}
	username := strings.TrimSpace(in.Username)
	rawSecret := strings.TrimSpace(in.Secret)
	nowStr := time.Now().UTC().Format(time.RFC3339)

	// When testing edits to an existing connection with blank secret, reuse its stored secret.
	var existingConn *domain.IntegrationConnection
	if strings.TrimSpace(in.ConnectionID) != "" {
		if store, _, err := s.requireStore(); err == nil {
			if c, err := store.GetConnection(ctx, strings.TrimSpace(in.ConnectionID)); err == nil {
				existingConn = &c
			}
		}
	}

	switch driver {
	case "gitlab":
		if authType == "oauth_password" {
			if rawSecret == "" && existingConn != nil && existingConn.AuthType == "oauth_password" {
				testConn := *existingConn
				testConn.BaseURL = baseURL
				testConn.Username = username
				client, err := s.buildGitLabClient(ctx, testConn)
				if err != nil {
					return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
				}
				identity, err := client.TestConnection(ctx)
				if err != nil {
					return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
				}
				return ConnectionTestResult{OK: true, Identity: identity, CheckedAt: nowStr}, nil
			}
			if username == "" || rawSecret == "" {
				return ConnectionTestResult{OK: false, Error: "GitLab OAuth2 密码登录需填写用户名和密码", CheckedAt: nowStr}, nil
			}
			session, err := gitlab.New(gitlab.Config{BaseURL: baseURL}).ExchangePasswordToken(ctx, username, rawSecret)
			if err != nil {
				return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
			}
			identity, err := gitlab.New(gitlab.Config{
				BaseURL:  baseURL,
				AuthType: authType,
				Username: username,
				Secret:   session.AccessToken,
			}).TestConnection(ctx)
			if err != nil {
				return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
			}
			return ConnectionTestResult{OK: true, Identity: identity, CheckedAt: nowStr}, nil
		}
		effectiveSec := rawSecret
		if effectiveSec == "" && existingConn != nil {
			effectiveSec, _ = s.resolveSecret(existingConn.SecretRef)
		}
		if effectiveSec == "" {
			return ConnectionTestResult{OK: false, Error: "请填写 Personal Access Token", CheckedAt: nowStr}, nil
		}
		identity, err := gitlab.New(gitlab.Config{
			BaseURL:  baseURL,
			AuthType: authType,
			Username: username,
			Secret:   effectiveSec,
		}).TestConnection(ctx)
		if err != nil {
			return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
		}
		return ConnectionTestResult{OK: true, Identity: identity, CheckedAt: nowStr}, nil

	case "github":
		effectiveSec := rawSecret
		if effectiveSec == "" && existingConn != nil {
			effectiveSec, _ = s.resolveSecret(existingConn.SecretRef)
		}
		if effectiveSec == "" {
			return ConnectionTestResult{OK: false, Error: "请填写 GitHub Personal Access Token", CheckedAt: nowStr}, nil
		}
		identity, err := github.New(github.Config{
			BaseURL:  baseURL,
			AuthType: authType,
			Username: username,
			Secret:   effectiveSec,
		}).TestConnection(ctx)
		if err != nil {
			return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
		}
		return ConnectionTestResult{OK: true, Identity: identity, CheckedAt: nowStr}, nil

	case "jira":
		effectiveSec := rawSecret
		if effectiveSec == "" && existingConn != nil {
			effectiveSec, _ = s.resolveSecret(existingConn.SecretRef)
		}
		if authType == "basic" && (username == "" || effectiveSec == "") {
			return ConnectionTestResult{OK: false, Error: "Jira Basic 认证需填写用户名和密码/Token", CheckedAt: nowStr}, nil
		}
		if authType == "pat" && effectiveSec == "" {
			return ConnectionTestResult{OK: false, Error: "请填写 Jira PAT", CheckedAt: nowStr}, nil
		}
		client := jira.New(jira.Config{
			BaseURL:  baseURL,
			AuthType: authType,
			Username: username,
			Secret:   effectiveSec,
		})
		identity, err := client.TestConnection(ctx)
		if err != nil {
			return ConnectionTestResult{OK: false, Error: err.Error(), CheckedAt: nowStr}, nil
		}
		statuses, _ := client.ListStatuses(ctx)
		return ConnectionTestResult{OK: true, Identity: identity, CheckedAt: nowStr, Statuses: statuses}, nil
	default:
		return ConnectionTestResult{}, fmt.Errorf("%w: 不支持的驱动 %q", ErrValidation, driver)
	}
}

func (s *Service) TestConnection(ctx context.Context, id string) (ConnectionTestResult, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return ConnectionTestResult{}, err
	}
	conn, err := store.GetConnection(ctx, id)
	if err != nil {
		return ConnectionTestResult{}, err
	}
	secretVal, _ := s.resolveSecret(conn.SecretRef)

	nowStr := time.Now().UTC().Format(time.RFC3339)
	var identity string
	var testErr error

	switch conn.Driver {
	case "gitlab":
		client, err := s.buildGitLabClient(ctx, conn)
		if err != nil {
			testErr = err
		} else {
			identity, testErr = client.TestConnection(ctx)
		}
	case "github":
		client := github.New(github.Config{
			ID:       conn.ID,
			Name:     conn.Name,
			BaseURL:  conn.BaseURL,
			AuthType: conn.AuthType,
			Username: conn.Username,
			Secret:   secretVal,
		})
		identity, testErr = client.TestConnection(ctx)
	case "jira":
		client := jira.New(jira.Config{
			ID:       conn.ID,
			Name:     conn.Name,
			BaseURL:  conn.BaseURL,
			AuthType: conn.AuthType,
			Username: conn.Username,
			Secret:   secretVal,
		})
		identity, testErr = client.TestConnection(ctx)
	default:
		testErr = fmt.Errorf("不支持的驱动 %q", conn.Driver)
	}

	conn.LastCheckedAt = nowStr
	conn.UpdatedAt = time.Now().UTC()
	if testErr != nil {
		conn.LastError = testErr.Error()
		_, _ = store.UpdateConnection(ctx, conn)
		return ConnectionTestResult{OK: false, Error: testErr.Error(), CheckedAt: nowStr}, nil
	}
	conn.LastError = ""
	_, _ = store.UpdateConnection(ctx, conn)
	return ConnectionTestResult{OK: true, Identity: identity, CheckedAt: nowStr}, nil
}

// --- GitLab Remote Queries ---

func validateConnectionAuthType(driver, authType string) error {
	switch driver {
	case "gitlab":
		if authType != "pat" && authType != "oauth_password" {
			return fmt.Errorf("%w: GitLab REST API v4 不支持账号密码 Basic Auth，请使用 pat 或 oauth_password", ErrValidation)
		}
	case "github":
		if authType != "pat" {
			return fmt.Errorf("%w: GitHub REST API 仅支持 Personal Access Token (pat)", ErrValidation)
		}
	case "jira":
		if authType != "pat" && authType != "basic" {
			return fmt.Errorf("%w: Jira 认证方式仅支持 pat 或 basic", ErrValidation)
		}
	default:
		return fmt.Errorf("%w: 不支持的驱动 %q", ErrValidation, driver)
	}
	return nil
}

func (s *Service) buildGitLabClient(ctx context.Context, conn domain.IntegrationConnection) (*gitlab.Client, error) {
	sec, _ := s.resolveSecret(conn.SecretRef)
	if conn.AuthType == "oauth_password" {
		if strings.TrimSpace(sec) == "" {
			return nil, fmt.Errorf("GitLab OAuth2 会话不存在，请重新配置密码登录或改用 PAT")
		}
		var session gitlab.OAuthSession
		if err := json.Unmarshal([]byte(sec), &session); err != nil || session.AccessToken == "" {
			return nil, fmt.Errorf("GitLab OAuth2 会话数据无效，请重新配置连接")
		}
		if session.ExpiresAt > 0 && time.Now().Unix()+30 >= session.ExpiresAt {
			if session.RefreshToken == "" {
				return nil, fmt.Errorf("GitLab OAuth2 令牌已过期且无 refresh_token，请重新登录或改用 PAT")
			}
			refreshed, err := gitlab.New(gitlab.Config{BaseURL: conn.BaseURL}).RefreshOAuthToken(ctx, session.RefreshToken)
			if err != nil {
				return nil, err
			}
			session = refreshed
			if enc, err := json.Marshal(session); err == nil && s.secretStore != nil && conn.SecretRef != "" {
				_ = s.secretStore.Set(conn.SecretRef, string(enc))
			}
		}
		sec = session.AccessToken
	}
	return gitlab.New(gitlab.Config{
		ID:       conn.ID,
		Name:     conn.Name,
		BaseURL:  conn.BaseURL,
		AuthType: conn.AuthType,
		Username: conn.Username,
		Secret:   sec,
	}), nil
}

func (s *Service) buildGitHubClient(conn domain.IntegrationConnection) *github.Client {
	sec, _ := s.resolveSecret(conn.SecretRef)
	return github.New(github.Config{
		ID:       conn.ID,
		Name:     conn.Name,
		BaseURL:  conn.BaseURL,
		AuthType: conn.AuthType,
		Username: conn.Username,
		Secret:   sec,
	})
}

func (s *Service) getCodeHostConn(ctx context.Context, connID string) (domain.IntegrationConnection, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	conn, err := store.GetConnection(ctx, connID)
	if err != nil {
		return domain.IntegrationConnection{}, err
	}
	if conn.Kind != "code_host" || (conn.Driver != "gitlab" && conn.Driver != "github") {
		return conn, fmt.Errorf("%w: 该连接不是支持的代码托管实例 (gitlab/github)", ErrValidation)
	}
	return conn, nil
}

// CodeHostProjects lists repositories/projects from either a GitLab or GitHub connection.
func (s *Service) CodeHostProjects(ctx context.Context, connID, search string) ([]gitlab.Project, error) {
	conn, err := s.getCodeHostConn(ctx, connID)
	if err != nil {
		return nil, err
	}
	switch conn.Driver {
	case "gitlab":
		client, err := s.buildGitLabClient(ctx, conn)
		if err != nil {
			return nil, err
		}
		return client.ListProjects(ctx, search, 30)
	case "github":
		ghProjects, err := s.buildGitHubClient(conn).ListProjects(ctx, search, 30)
		if err != nil {
			return nil, err
		}
		out := make([]gitlab.Project, 0, len(ghProjects))
		for _, p := range ghProjects {
			out = append(out, gitlab.Project{
				ID:                p.ID,
				Name:              p.Name,
				PathWithNamespace: p.PathWithNamespace,
				DefaultBranch:     p.DefaultBranch,
				WebURL:            p.WebURL,
			})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: 不支持的代码托管驱动 %q", ErrValidation, conn.Driver)
	}
}

// CodeHostMergeRequests lists GitLab MRs or GitHub PRs using a unified contract.
func (s *Service) CodeHostMergeRequests(ctx context.Context, connID, project, scope, state, search string) ([]components.MergeRequest, error) {
	conn, err := s.getCodeHostConn(ctx, connID)
	if err != nil {
		return nil, err
	}
	switch conn.Driver {
	case "gitlab":
		client, err := s.buildGitLabClient(ctx, conn)
		if err != nil {
			return nil, err
		}
		return client.QueryMergeRequests(ctx, project, scope, state, search, 20)
	case "github":
		return s.buildGitHubClient(conn).QueryPullRequests(ctx, project, scope, state, search, 20)
	default:
		return nil, fmt.Errorf("%w: 不支持的代码托管驱动 %q", ErrValidation, conn.Driver)
	}
}

// CodeHostRemoteCommits lists remote branch/merged-PR commits from GitLab or GitHub.
func (s *Service) CodeHostRemoteCommits(ctx context.Context, connID, project, branch string) ([]components.RemoteCommit, error) {
	conn, err := s.getCodeHostConn(ctx, connID)
	if err != nil {
		return nil, err
	}
	authors := splitComma(conn.Options["author_aliases"])
	if len(authors) == 0 {
		authors = s.Config().Git.AuthorAliases
	}
	switch conn.Driver {
	case "gitlab":
		client, err := s.buildGitLabClient(ctx, conn)
		if err != nil {
			return nil, err
		}
		return client.ListRemoteCommits(ctx, project, branch, authors)
	case "github":
		return s.buildGitHubClient(conn).ListRemoteCommits(ctx, project, branch, authors)
	default:
		return nil, fmt.Errorf("%w: 不支持的代码托管驱动 %q", ErrValidation, conn.Driver)
	}
}

// Backward-compatible GitLab-named wrappers delegating to unified CodeHost methods.
func (s *Service) GitLabProjects(ctx context.Context, connID, search string) ([]gitlab.Project, error) {
	return s.CodeHostProjects(ctx, connID, search)
}

func (s *Service) GitLabMergeRequests(ctx context.Context, connID, project, scope, state, search string) ([]components.MergeRequest, error) {
	return s.CodeHostMergeRequests(ctx, connID, project, scope, state, search)
}

func (s *Service) GitLabRemoteCommits(ctx context.Context, connID, project, branch string) ([]components.RemoteCommit, error) {
	return s.CodeHostRemoteCommits(ctx, connID, project, branch)
}

// --- Jira Remote Queries & Local Followups ---

type JiraIssueWithFollowup struct {
	components.IssueItem
	LocalState string `json:"local_state"`
	Reason     string `json:"reason"`
}

func (s *Service) JiraSearchIssues(ctx context.Context, connID string, q components.IssueQuery) ([]JiraIssueWithFollowup, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	conn, err := store.GetConnection(ctx, connID)
	if err != nil {
		return nil, err
	}
	if conn.Driver != "jira" {
		return nil, fmt.Errorf("%w: 该连接不是 Jira 实例", ErrValidation)
	}
	// Apply configurable default inspection status whitelist if caller did not specify statuses.
	if len(q.Statuses) == 0 {
		q.Statuses = splitComma(conn.Options["inspection_statuses"])
	}
	if strings.TrimSpace(q.ProjectKey) == "" {
		q.ProjectKey = strings.TrimSpace(conn.Options["default_project"])
	}

	sec, _ := s.resolveSecret(conn.SecretRef)
	client := jira.New(jira.Config{
		ID:       conn.ID,
		Name:     conn.Name,
		BaseURL:  conn.BaseURL,
		AuthType: conn.AuthType,
		Username: conn.Username,
		Secret:   sec,
	})
	issues, err := client.SearchIssues(ctx, q)
	if err != nil {
		return nil, err
	}

	followups, err := store.ListIssueFollowups(ctx, connID, "")
	if err != nil {
		return nil, err
	}
	byKey := map[string]domain.IssueFollowup{}
	for _, f := range followups {
		byKey[f.IssueKey] = f
	}

	out := make([]JiraIssueWithFollowup, 0, len(issues))
	for _, item := range issues {
		localState := "active"
		reason := ""
		if f, ok := byKey[item.Key]; ok {
			localState = f.LocalState
			reason = f.Reason
		}
		out = append(out, JiraIssueWithFollowup{
			IssueItem:  item,
			LocalState: localState,
			Reason:     reason,
		})
	}
	return out, nil
}

type SetFollowupInput struct {
	ConnectionID string `json:"connection_id"`
	IssueKey     string `json:"issue_key"`
	Title        string `json:"title"`
	WebURL       string `json:"web_url"`
	RemoteState  string `json:"remote_state"`
	LocalState   string `json:"local_state"` // "active" | "paused" | "pending_retest" | "verified"
	Reason       string `json:"reason"`
}

func (s *Service) SetIssueFollowup(ctx context.Context, in SetFollowupInput) (domain.IssueFollowup, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.IssueFollowup{}, err
	}
	key := strings.TrimSpace(in.IssueKey)
	if in.ConnectionID == "" || key == "" {
		return domain.IssueFollowup{}, fmt.Errorf("%w: connection_id 与 issue_key 不能为空", ErrValidation)
	}
	state := strings.TrimSpace(in.LocalState)
	switch state {
	case "active", "paused", "pending_retest", "verified":
	default:
		return domain.IssueFollowup{}, fmt.Errorf("%w: local_state 仅支持 active / paused / pending_retest / verified", ErrValidation)
	}
	now := time.Now().UTC()
	return store.UpsertIssueFollowup(ctx, domain.IssueFollowup{
		ID:           uuid.NewString(),
		ConnectionID: in.ConnectionID,
		IssueKey:     key,
		Title:        strings.TrimSpace(in.Title),
		WebURL:       strings.TrimSpace(in.WebURL),
		RemoteState:  strings.TrimSpace(in.RemoteState),
		LocalState:   state,
		Reason:       strings.TrimSpace(in.Reason),
		Revision:     strings.TrimSpace(in.RemoteState),
		UpdatedAt:    now,
		CreatedAt:    now,
	})
}

func (s *Service) ListIssueFollowups(ctx context.Context, connID, localState string) ([]domain.IssueFollowup, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListIssueFollowups(ctx, strings.TrimSpace(connID), strings.TrimSpace(localState))
}

// --- Task Remote Links ---

type CreateRemoteLinkInput struct {
	ConnectionID string `json:"connection_id"`
	RemoteKind   string `json:"remote_kind"` // "issue" | "merge_request"
	RemoteKey    string `json:"remote_key"`
	Title        string `json:"title"`
	RemoteState  string `json:"remote_state"`
	WebURL       string `json:"web_url"`
}

func (s *Service) LinkRemote(ctx context.Context, taskID string, in CreateRemoteLinkInput) (domain.RemoteLink, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.RemoteLink{}, err
	}
	if _, err := store.GetTask(ctx, taskID); err != nil {
		return domain.RemoteLink{}, err
	}
	if in.ConnectionID == "" || strings.TrimSpace(in.RemoteKey) == "" {
		return domain.RemoteLink{}, fmt.Errorf("%w: connection_id 与 remote_key 不能为空", ErrValidation)
	}
	if in.RemoteKind != "issue" && in.RemoteKind != "merge_request" {
		return domain.RemoteLink{}, fmt.Errorf("%w: remote_kind 仅支持 issue 或 merge_request", ErrValidation)
	}
	now := time.Now().UTC()
	return store.UpsertRemoteLink(ctx, domain.RemoteLink{
		ID:           uuid.NewString(),
		TaskID:       taskID,
		ConnectionID: in.ConnectionID,
		RemoteKind:   in.RemoteKind,
		RemoteKey:    strings.TrimSpace(in.RemoteKey),
		Title:        strings.TrimSpace(in.Title),
		RemoteState:  strings.TrimSpace(in.RemoteState),
		WebURL:       strings.TrimSpace(in.WebURL),
		SyncedAt:     now,
		CreatedAt:    now,
	})
}

func (s *Service) UnlinkRemote(ctx context.Context, taskID, linkID string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	return store.DeleteRemoteLink(ctx, taskID, linkID)
}

// --- Generic Knowledge Bases (Local Dir & Remote Git + FTS5) ---

type CreateKnowledgeBaseInput struct {
	Name       string `json:"name"`
	SourceType string `json:"source_type"` // "local_dir" | "remote_git"
	Location   string `json:"location"`
	Branch     string `json:"branch"`
	Secret     string `json:"secret"`
	ReadOnly   bool   `json:"readonly"`
}

func (s *Service) ListKnowledgeBases(ctx context.Context) ([]domain.KnowledgeBase, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListKnowledgeBases(ctx)
}

func (s *Service) CreateKnowledgeBase(ctx context.Context, in CreateKnowledgeBaseInput) (domain.KnowledgeBase, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	name := strings.TrimSpace(in.Name)
	srcType := strings.TrimSpace(in.SourceType)
	loc := strings.TrimSpace(in.Location)
	if name == "" || loc == "" {
		return domain.KnowledgeBase{}, fmt.Errorf("%w: 知识库名称与路径/仓库地址不能为空", ErrValidation)
	}
	if srcType != "local_dir" && srcType != "remote_git" {
		return domain.KnowledgeBase{}, fmt.Errorf("%w: source_type 仅支持 local_dir 或 remote_git", ErrValidation)
	}

	id := uuid.NewString()
	secretRef := ""
	if rawSec := strings.TrimSpace(in.Secret); rawSec != "" {
		if s.secretStore == nil {
			return domain.KnowledgeBase{}, errors.New("凭据存储不可用")
		}
		secretRef = "kb." + id + ".secret"
		if err := s.secretStore.Set(secretRef, rawSec); err != nil {
			return domain.KnowledgeBase{}, fmt.Errorf("保存知识库凭据失败: %w", err)
		}
	}

	readonly := in.ReadOnly
	mirrorDir := ""
	if srcType == "remote_git" {
		mirrorDir = filepath.Join(cfg.Storage.DataDir, "kb-mirrors", id)
	}

	now := time.Now().UTC()
	kb, err := store.CreateKnowledgeBase(ctx, domain.KnowledgeBase{
		ID:         id,
		Name:       name,
		SourceType: srcType,
		Location:   loc,
		Branch:     strings.TrimSpace(in.Branch),
		SecretRef:  secretRef,
		HasSecret:  secretRef != "",
		ReadOnly:   readonly,
		MirrorDir:  mirrorDir,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		if secretRef != "" && s.secretStore != nil {
			_ = s.secretStore.Delete(secretRef)
		}
		return domain.KnowledgeBase{}, err
	}

	// Perform initial sync/indexing right away so user sees real feedback immediately.
	synced, _ := s.SyncKnowledgeBase(ctx, kb.ID)
	if synced.ID != "" {
		return synced, nil
	}
	return store.GetKnowledgeBase(ctx, kb.ID)
}

func (s *Service) SyncKnowledgeBase(ctx context.Context, kbID string) (domain.KnowledgeBase, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.KnowledgeBase{}, err
	}
	kb, err := store.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return domain.KnowledgeBase{}, err
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)
	commitSHA := kb.LastCommitSHA
	if kb.SourceType == "remote_git" {
		sec, _ := s.resolveSecret(kb.SecretRef)
		sha, syncErr := knowledge.SyncRemoteGit(ctx, kb, sec)
		if syncErr != nil {
			_ = store.UpdateKnowledgeBaseSync(ctx, kb.ID, commitSHA, nowStr, syncErr.Error())
			return store.GetKnowledgeBase(ctx, kb.ID)
		}
		commitSHA = sha
	}

	docs, scanErr := knowledge.ScanDocs(kb.ID, knowledge.RootDir(kb))
	if scanErr != nil {
		_ = store.UpdateKnowledgeBaseSync(ctx, kb.ID, commitSHA, nowStr, scanErr.Error())
		return store.GetKnowledgeBase(ctx, kb.ID)
	}
	if err := store.ReplaceKnowledgeDocs(ctx, kb.ID, docs); err != nil {
		_ = store.UpdateKnowledgeBaseSync(ctx, kb.ID, commitSHA, nowStr, err.Error())
		return store.GetKnowledgeBase(ctx, kb.ID)
	}
	_ = store.UpdateKnowledgeBaseSync(ctx, kb.ID, commitSHA, nowStr, "")
	return store.GetKnowledgeBase(ctx, kb.ID)
}

func (s *Service) DeleteKnowledgeBase(ctx context.Context, kbID string) error {
	store, _, err := s.requireStore()
	if err != nil {
		return err
	}
	kb, err := store.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return err
	}
	if err := store.DeleteKnowledgeBase(ctx, kbID); err != nil {
		return err
	}
	if kb.SecretRef != "" && s.secretStore != nil {
		_ = s.secretStore.Delete(kb.SecretRef)
	}
	if kb.SourceType == "remote_git" && kb.MirrorDir != "" {
		_ = os.RemoveAll(kb.MirrorDir)
	}
	return nil
}

func (s *Service) ListKnowledgeDocs(ctx context.Context, kbID string) ([]domain.KnowledgeDoc, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.ListKnowledgeDocs(ctx, kbID)
}

func (s *Service) ReadKnowledgeDoc(ctx context.Context, kbID, relPath string) (domain.KnowledgeDocContent, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	kb, err := store.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	doc, err := knowledge.ReadDoc(kb, relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.KnowledgeDocContent{}, fmt.Errorf("%w: 文档不存在", storage.ErrNotFound)
		}
		return domain.KnowledgeDocContent{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return doc, nil
}

func (s *Service) SaveKnowledgeDoc(ctx context.Context, kbID, relPath, content string) (domain.KnowledgeDocContent, error) {
	store, cfg, err := s.requireStore()
	if err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	kb, err := store.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	if kb.ReadOnly {
		return domain.KnowledgeDocContent{}, fmt.Errorf("%w: 当前知识库为只读模式，不允许写入", ErrValidation)
	}

	if kb.SourceType == "remote_git" {
		sec, _ := s.resolveSecret(kb.SecretRef)
		authorName, authorEmail := resolveGitAuthor(cfg.Git.AuthorAliases)
		res, err := knowledge.WriteAndPushRemoteGit(ctx, kb, relPath, content, sec, authorName, authorEmail)
		if err != nil {
			return domain.KnowledgeDocContent{}, fmt.Errorf("%w: %v", ErrValidation, err)
		}
		nowStr := time.Now().UTC().Format(time.RFC3339)
		if res.PulledRemote {
			if docs, scanErr := knowledge.ScanDocs(kb.ID, knowledge.RootDir(kb)); scanErr == nil {
				_ = store.ReplaceKnowledgeDocs(ctx, kb.ID, docs)
			}
		} else {
			if err := store.UpsertKnowledgeDoc(ctx, res.Doc); err != nil {
				return domain.KnowledgeDocContent{}, err
			}
		}
		_ = store.UpdateKnowledgeBaseSync(ctx, kb.ID, res.CommitSHA, nowStr, "")
		return domain.KnowledgeDocContent{Doc: res.Doc.Doc, Content: content}, nil
	}

	indexed, err := knowledge.WriteDoc(kb, relPath, content)
	if err != nil {
		return domain.KnowledgeDocContent{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if err := store.UpsertKnowledgeDoc(ctx, indexed); err != nil {
		return domain.KnowledgeDocContent{}, err
	}
	return domain.KnowledgeDocContent{Doc: indexed.Doc, Content: content}, nil
}

func resolveGitAuthor(aliases []string) (name, email string) {
	for _, a := range aliases {
		t := strings.TrimSpace(a)
		if t == "" {
			continue
		}
		if strings.Contains(t, "@") {
			if email == "" {
				email = t
			}
		} else if name == "" {
			name = t
		}
	}
	if name == "" {
		name = "Lagrangian"
	}
	if email == "" {
		email = "lagrangian@local"
	}
	return name, email
}

func (s *Service) SearchKnowledge(ctx context.Context, query, kbID string, limit int) ([]domain.KnowledgeSearchHit, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return nil, err
	}
	return store.SearchKnowledge(ctx, query, strings.TrimSpace(kbID), limit)
}

func (s *Service) KnowledgeGraph(ctx context.Context, kbID string) (domain.KnowledgeGraph, error) {
	store, _, err := s.requireStore()
	if err != nil {
		return domain.KnowledgeGraph{}, err
	}
	kb, err := store.GetKnowledgeBase(ctx, kbID)
	if err != nil {
		return domain.KnowledgeGraph{}, err
	}
	docs, err := store.ListKnowledgeDocBodies(ctx, kbID)
	if err != nil {
		return domain.KnowledgeGraph{}, err
	}
	return knowledge.BuildGraph(kb, docs), nil
}

// --- Helpers ---

func (s *Service) resolveSecret(ref string) (string, error) {
	if ref == "" || s.secretStore == nil {
		return "", nil
	}
	val, err := s.secretStore.Get(ref)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil
	}
	return val, err
}

func splitComma(raw string) []string {
	raw = strings.ReplaceAll(raw, "，", ",")
	raw = strings.ReplaceAll(raw, "\n", ",")
	parts := strings.Split(raw, ",")
	return cleanStrings(parts)
}

// livePhase2Statuses enriches component registry status with real DB-backed
// GitLab/Jira connections and KnowledgeBases for Settings & OrbitView.
func (s *Service) livePhase2Statuses(ctx context.Context) []components.Status {
	base := s.registry.Snapshot(ctx)
	store, _, err := s.requireStore()
	if err != nil {
		return base
	}
	conns, _ := store.ListConnections(ctx, "")
	kbs, _ := store.ListKnowledgeBases(ctx)
	now := time.Now().UTC()

	hasCodeHostConn := false
	hasIssueConn := false
	for _, c := range conns {
		if c.Kind == "code_host" {
			hasCodeHostConn = true
		}
		if c.Kind == "issue_tracker" {
			hasIssueConn = true
		}
	}

	out := make([]components.Status, 0, len(base)+len(conns)+len(kbs))
	for _, st := range base {
		// Replace placeholder unconfigured entries when live DB instances exist.
		if st.Kind == components.KindIssueTracker && hasIssueConn && strings.HasSuffix(st.ID, ":unconfigured") {
			continue
		}
		if st.Kind == components.KindKnowledgeBase && len(kbs) > 0 && strings.HasSuffix(st.ID, ":unconfigured") {
			continue
		}
		if st.Kind == components.KindCodeHost && hasCodeHostConn && st.ID == "code_host:local-git" {
			// Keep local-git alongside remote GitLab connections
		}
		out = append(out, st)
	}

	for _, c := range conns {
		state := components.HealthDisabled
		detail := "连接已停用"
		if c.Enabled {
			if c.LastError != "" {
				state = components.HealthDegraded
				detail = c.LastError
			} else {
				state = components.HealthReady
				detail = c.BaseURL
			}
		}
		out = append(out, components.Status{
			ID:        c.Kind + ":" + c.ID,
			Kind:      components.Kind(c.Kind),
			Driver:    c.Driver,
			Name:      c.Name,
			Enabled:   c.Enabled,
			State:     state,
			Detail:    detail,
			CheckedAt: now,
		})
	}

	for _, kb := range kbs {
		state := components.HealthReady
		detail := fmt.Sprintf("%s · %d 篇文档", kb.SourceType, kb.DocCount)
		if kb.LastError != "" {
			state = components.HealthDegraded
			detail = kb.LastError
		}
		out = append(out, components.Status{
			ID:        "knowledge_base:" + kb.ID,
			Kind:      components.KindKnowledgeBase,
			Driver:    kb.SourceType,
			Name:      kb.Name,
			Enabled:   true,
			State:     state,
			Detail:    detail,
			CheckedAt: now,
		})
	}
	return out
}
