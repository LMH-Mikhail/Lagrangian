// Package gitlab implements the components.CodeHost adapter for GitLab REST API v4.
// Supports Personal Access Token (PRIVATE-TOKEN) and OAuth2 password grant (/oauth/token -> Bearer),
// project listing, merge request queries, and remote branch/MR commit lookup.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/components"
)

type Config struct {
	ID       string
	Name     string
	BaseURL  string
	AuthType string // "pat" | "oauth_password"
	Username string
	Secret   string // PAT or OAuth2 access_token
}

type OAuthSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
}

type Client struct {
	cfg  Config
	http *http.Client
}

type Project struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	WebURL            string `json:"web_url"`
}

func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) ID() string     { return c.cfg.ID }
func (c *Client) Kind() components.Kind { return components.KindCodeHost }
func (c *Client) Driver() string { return "gitlab" }
func (c *Client) Name() string   { return c.cfg.Name }

func (c *Client) Health(ctx context.Context) components.Status {
	user, err := c.TestConnection(ctx)
	now := time.Now().UTC()
	if err != nil {
		return components.Status{
			ID:        c.cfg.ID,
			Kind:      components.KindCodeHost,
			Driver:    "gitlab",
			Name:      c.cfg.Name,
			Enabled:   true,
			State:     components.HealthDegraded,
			Detail:    err.Error(),
			CheckedAt: now,
		}
	}
	return components.Status{
		ID:        c.cfg.ID,
		Kind:      components.KindCodeHost,
		Driver:    "gitlab",
		Name:      c.cfg.Name,
		Enabled:   true,
		State:     components.HealthReady,
		Detail:    fmt.Sprintf("已连接 GitLab (%s)", user),
		CheckedAt: now,
	}
}

// TestConnection verifies credentials against GET /api/v4/user.
func (c *Client) TestConnection(ctx context.Context) (string, error) {
	var resp struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := c.getJSON(ctx, "/api/v4/user", nil, &resp); err != nil {
		return "", err
	}
	if resp.Username != "" {
		return resp.Username, nil
	}
	if resp.Name != "" {
		return resp.Name, nil
	}
	return "authenticated", nil
}

func (c *Client) ListProjects(ctx context.Context, search string, limit int) ([]Project, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := url.Values{}
	q.Set("membership", "true")
	q.Set("simple", "true")
	q.Set("order_by", "last_activity_at")
	q.Set("per_page", fmt.Sprintf("%d", limit))
	if s := strings.TrimSpace(search); s != "" {
		q.Set("search", s)
	}
	var out []Project
	if err := c.getJSON(ctx, "/api/v4/projects", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListMergeRequests implements components.CodeHost with default scope.
func (c *Client) ListMergeRequests(ctx context.Context, project, state string) ([]components.MergeRequest, error) {
	return c.QueryMergeRequests(ctx, project, "all", state, "", 20)
}

// QueryMergeRequests queries merge requests globally or for a specific project path/ID.
func (c *Client) QueryMergeRequests(ctx context.Context, project, scope, state, search string, limit int) ([]components.MergeRequest, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := url.Values{}
	if scope != "" {
		q.Set("scope", scope) // "assigned_to_me" | "created_by_me" | "all"
	}
	if state != "" && state != "all" {
		q.Set("state", state) // "opened" | "merged" | "closed"
	}
	if s := strings.TrimSpace(search); s != "" {
		q.Set("search", s)
	}
	q.Set("order_by", "updated_at")
	q.Set("per_page", fmt.Sprintf("%d", limit))

	endpoint := "/api/v4/merge_requests"
	if p := strings.TrimSpace(project); p != "" {
		endpoint = "/api/v4/projects/" + url.PathEscape(p) + "/merge_requests"
	}

	var raw []struct {
		IID          int    `json:"iid"`
		Title        string `json:"title"`
		State        string `json:"state"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		WebURL       string `json:"web_url"`
		References   struct {
			Full string `json:"full"`
		} `json:"references"`
	}
	if err := c.getJSON(ctx, endpoint, q, &raw); err != nil {
		return nil, err
	}

	out := make([]components.MergeRequest, 0, len(raw))
	for _, mr := range raw {
		id := mr.References.Full
		if id == "" {
			if project != "" {
				id = fmt.Sprintf("%s!%d", project, mr.IID)
			} else {
				id = fmt.Sprintf("!%d", mr.IID)
			}
		}
		out = append(out, components.MergeRequest{
			ID:           id,
			Title:        mr.Title,
			State:        mr.State,
			SourceBranch: mr.SourceBranch,
			TargetBranch: mr.TargetBranch,
			WebURL:       mr.WebURL,
		})
	}
	return out, nil
}

// ListRemoteCommits reads commits for a remote branch, falling back to merged MRs
// with the same source branch if the remote branch was already deleted after merge.
func (c *Client) ListRemoteCommits(ctx context.Context, project, branch string, authors []string) ([]components.RemoteCommit, error) {
	projEsc := url.PathEscape(strings.TrimSpace(project))
	if projEsc == "" {
		return nil, fmt.Errorf("GitLab 项目路径或 ID 不能为空")
	}

	q := url.Values{}
	if b := strings.TrimSpace(branch); b != "" {
		q.Set("ref_name", b)
	}
	q.Set("per_page", "50")

	type glCommit struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		AuthorName  string `json:"author_name"`
		AuthorEmail string `json:"author_email"`
		CommittedAt string `json:"committed_date"`
		WebURL      string `json:"web_url"`
	}

	var rawCommits []glCommit
	err := c.getJSON(ctx, "/api/v4/projects/"+projEsc+"/repository/commits", q, &rawCommits)
	if err != nil && strings.TrimSpace(branch) != "" {
		// Fallback: look up merged MRs originating from the deleted branch.
		mrQ := url.Values{}
		mrQ.Set("source_branch", strings.TrimSpace(branch))
		mrQ.Set("state", "merged")
		mrQ.Set("per_page", "5")
		var mrs []struct {
			IID int `json:"iid"`
		}
		if mrErr := c.getJSON(ctx, "/api/v4/projects/"+projEsc+"/merge_requests", mrQ, &mrs); mrErr == nil && len(mrs) > 0 {
			seen := map[string]bool{}
			for _, mr := range mrs {
				var mrCommits []glCommit
				if cmErr := c.getJSON(ctx, fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/commits", projEsc, mr.IID), nil, &mrCommits); cmErr == nil {
					for _, cm := range mrCommits {
						if !seen[cm.ID] {
							seen[cm.ID] = true
							rawCommits = append(rawCommits, cm)
						}
					}
				}
			}
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}

	aliases := normalizeAliases(authors)
	out := make([]components.RemoteCommit, 0, len(rawCommits))
	for _, cm := range rawCommits {
		if len(aliases) > 0 && !matchesAuthor(cm.AuthorName, cm.AuthorEmail, aliases) {
			continue
		}
		out = append(out, components.RemoteCommit{
			SHA:         cm.ID,
			Title:       cm.Title,
			Author:      cm.AuthorName,
			CommittedAt: cm.CommittedAt,
			WebURL:      cm.WebURL,
		})
	}
	return out, nil
}

// ExchangePasswordToken exchanges GitLab username + password for an OAuth2
// access/refresh token pair via POST /oauth/token (grant_type=password).
func (c *Client) ExchangePasswordToken(ctx context.Context, username, password string) (OAuthSession, error) {
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", strings.TrimSpace(username))
	form.Set("password", password)
	return c.postOAuthToken(ctx, form, "")
}

// RefreshOAuthToken refreshes an expired OAuth2 session using refresh_token.
func (c *Client) RefreshOAuthToken(ctx context.Context, refreshToken string) (OAuthSession, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return c.postOAuthToken(ctx, form, refreshToken)
}

func (c *Client) postOAuthToken(ctx context.Context, form url.Values, fallbackRefresh string) (OAuthSession, error) {
	if c.cfg.BaseURL == "" {
		return OAuthSession{}, fmt.Errorf("GitLab base_url 未配置")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return OAuthSession{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return OAuthSession{}, fmt.Errorf("GitLab OAuth 登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return OAuthSession{}, err
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return OAuthSession{}, fmt.Errorf("GitLab OAuth2 密码登录失败 (HTTP %d)：请检查用户名密码；若账号开启 2FA 或实例禁用密码授权，请改用 Personal Access Token (PAT)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return OAuthSession{}, fmt.Errorf("GitLab OAuth2 端点返回错误 (HTTP %d)", resp.StatusCode)
	}

	var parsed struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || strings.TrimSpace(parsed.AccessToken) == "" {
		return OAuthSession{}, fmt.Errorf("GitLab 未返回有效的 OAuth2 access_token，请确认该实例支持 /oauth/token 或改用 PAT")
	}
	refresh := strings.TrimSpace(parsed.RefreshToken)
	if refresh == "" {
		refresh = fallbackRefresh
	}
	var expiresAt int64
	if parsed.ExpiresIn > 0 {
		expiresAt = time.Now().Unix() + int64(parsed.ExpiresIn)
	}
	return OAuthSession{
		AccessToken:  parsed.AccessToken,
		RefreshToken: refresh,
		ExpiresAt:    expiresAt,
	}, nil
}

func (c *Client) getJSON(ctx context.Context, apiPath string, query url.Values, dst any) error {
	if c.cfg.BaseURL == "" {
		return fmt.Errorf("GitLab base_url 未配置")
	}
	u := c.cfg.BaseURL + apiPath
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.cfg.Secret != "" {
		if strings.EqualFold(c.cfg.AuthType, "oauth_password") {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Secret)
		} else {
			req.Header.Set("PRIVATE-TOKEN", c.cfg.Secret)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitLab 网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("GitLab 认证失败 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitLab 返回错误 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("解析 GitLab 响应失败: %w", err)
	}
	return nil
}

func normalizeAliases(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if t := strings.ToLower(strings.TrimSpace(a)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func matchesAuthor(name, email string, aliases []string) bool {
	ln := strings.ToLower(strings.TrimSpace(name))
	le := strings.ToLower(strings.TrimSpace(email))
	for _, a := range aliases {
		if ln == a || le == a || strings.Contains(le, a) {
			return true
		}
	}
	return false
}
