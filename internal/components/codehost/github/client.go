// Package github implements the components.CodeHost adapter for GitHub REST API v3
// (github.com and GitHub Enterprise Server). Supports Personal Access Token (Bearer),
// repository listing, pull request search/listing, and remote branch/PR commit lookup.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/components"
)

type Config struct {
	ID       string
	Name     string
	BaseURL  string // defaults to https://api.github.com
	AuthType string // "pat"
	Username string
	Secret   string
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

func NormalizeBaseURL(raw string) string {
	u := strings.TrimRight(strings.TrimSpace(raw), "/")
	if u == "" || strings.EqualFold(u, "https://github.com") || strings.EqualFold(u, "http://github.com") {
		return "https://api.github.com"
	}
	return u
}

func New(cfg Config) *Client {
	cfg.BaseURL = NormalizeBaseURL(cfg.BaseURL)
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) ID() string            { return c.cfg.ID }
func (c *Client) Kind() components.Kind { return components.KindCodeHost }
func (c *Client) Driver() string        { return "github" }
func (c *Client) Name() string          { return c.cfg.Name }

func (c *Client) Health(ctx context.Context) components.Status {
	user, err := c.TestConnection(ctx)
	now := time.Now().UTC()
	if err != nil {
		return components.Status{
			ID:        c.cfg.ID,
			Kind:      components.KindCodeHost,
			Driver:    "github",
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
		Driver:    "github",
		Name:      c.cfg.Name,
		Enabled:   true,
		State:     components.HealthReady,
		Detail:    fmt.Sprintf("已连接 GitHub (%s)", user),
		CheckedAt: now,
	}
}

// TestConnection verifies credentials against GET /user.
func (c *Client) TestConnection(ctx context.Context) (string, error) {
	var resp struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := c.getJSON(ctx, "/user", nil, &resp); err != nil {
		return "", err
	}
	if resp.Login != "" {
		return resp.Login, nil
	}
	if resp.Name != "" {
		return resp.Name, nil
	}
	return "authenticated", nil
}

// ListProjects returns repositories accessible to the authenticated user.
func (c *Client) ListProjects(ctx context.Context, search string, perPage int) ([]Project, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 30
	}
	q := url.Values{}
	q.Set("sort", "updated")
	q.Set("per_page", strconv.Itoa(perPage))
	var raw []struct {
		ID            int    `json:"id"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		HTMLURL       string `json:"html_url"`
	}
	if err := c.getJSON(ctx, "/user/repos", q, &raw); err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(search))
	out := make([]Project, 0, len(raw))
	for _, r := range raw {
		if needle != "" && !strings.Contains(strings.ToLower(r.FullName), needle) && !strings.Contains(strings.ToLower(r.Name), needle) {
			continue
		}
		out = append(out, Project{
			ID:                r.ID,
			Name:              r.Name,
			PathWithNamespace: r.FullName,
			DefaultBranch:     r.DefaultBranch,
			WebURL:            r.HTMLURL,
		})
	}
	return out, nil
}

// ListMergeRequests satisfies components.CodeHost using GitHub Pull Requests.
func (c *Client) ListMergeRequests(ctx context.Context, project string, state string) ([]components.MergeRequest, error) {
	return c.QueryPullRequests(ctx, project, "all", state, "", 20)
}

// QueryPullRequests lists PRs for a specific owner/repo or searches PRs across repositories.
func (c *Client) QueryPullRequests(ctx context.Context, repo, scope, state, search string, perPage int) ([]components.MergeRequest, error) {
	if perPage <= 0 || perPage > 100 {
		perPage = 20
	}
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	stateNorm := strings.ToLower(strings.TrimSpace(state))

	if repo != "" {
		owner, name, ok := splitRepo(repo)
		if !ok {
			return nil, fmt.Errorf("GitHub 仓库路径格式应为 owner/repo，当前为 %q", repo)
		}
		ghState := "open"
		switch stateNorm {
		case "", "opened", "open":
			ghState = "open"
		case "merged", "closed":
			ghState = "closed"
		case "all":
			ghState = "all"
		}
		q := url.Values{}
		q.Set("state", ghState)
		q.Set("sort", "updated")
		q.Set("direction", "desc")
		q.Set("per_page", strconv.Itoa(perPage))

		var raw []struct {
			Number   int    `json:"number"`
			Title    string `json:"title"`
			State    string `json:"state"`
			MergedAt string `json:"merged_at"`
			HTMLURL  string `json:"html_url"`
			Head     struct {
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(name))
		if err := c.getJSON(ctx, path, q, &raw); err != nil {
			return nil, err
		}
		needle := strings.ToLower(strings.TrimSpace(search))
		out := make([]components.MergeRequest, 0, len(raw))
		for _, pr := range raw {
			effectiveState := pr.State
			if pr.MergedAt != "" {
				effectiveState = "merged"
			}
			if stateNorm == "merged" && effectiveState != "merged" {
				continue
			}
			if stateNorm == "closed" && effectiveState == "merged" {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(pr.Title), needle) {
				continue
			}
			out = append(out, components.MergeRequest{
				ID:           fmt.Sprintf("%s#%d", repo, pr.Number),
				Title:        pr.Title,
				State:        effectiveState,
				SourceBranch: pr.Head.Ref,
				TargetBranch: pr.Base.Ref,
				WebURL:       pr.HTMLURL,
			})
		}
		return out, nil
	}

	// Global PR search via GET /search/issues
	terms := []string{"is:pr"}
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "assigned_to_me":
		terms = append(terms, "assignee:@me")
	case "created_by_me":
		terms = append(terms, "author:@me")
	default:
		terms = append(terms, "involves:@me")
	}
	switch stateNorm {
	case "", "opened", "open":
		terms = append(terms, "is:open")
	case "merged":
		terms = append(terms, "is:merged")
	case "closed":
		terms = append(terms, "is:closed", "is:unmerged")
	}
	if kw := strings.TrimSpace(search); kw != "" {
		terms = append(terms, kw)
	}
	q := url.Values{}
	q.Set("q", strings.Join(terms, " "))
	q.Set("sort", "updated")
	q.Set("order", "desc")
	q.Set("per_page", strconv.Itoa(perPage))

	var resp struct {
		Items []struct {
			Number      int    `json:"number"`
			Title       string `json:"title"`
			State       string `json:"state"`
			HTMLURL     string `json:"html_url"`
			PullRequest *struct {
				MergedAt string `json:"merged_at"`
			} `json:"pull_request"`
			RepositoryURL string `json:"repository_url"`
		} `json:"items"`
	}
	if err := c.getJSON(ctx, "/search/issues", q, &resp); err != nil {
		return nil, err
	}
	out := make([]components.MergeRequest, 0, len(resp.Items))
	for _, it := range resp.Items {
		st := it.State
		if it.PullRequest != nil && it.PullRequest.MergedAt != "" {
			st = "merged"
		}
		prefix := extractRepoFromAPIURL(it.RepositoryURL)
		id := fmt.Sprintf("#%d", it.Number)
		if prefix != "" {
			id = fmt.Sprintf("%s#%d", prefix, it.Number)
		}
		out = append(out, components.MergeRequest{
			ID:      id,
			Title:   it.Title,
			State:   st,
			WebURL:  it.HTMLURL,
		})
	}
	return out, nil
}

// ListRemoteCommits queries commits on a GitHub branch. If the branch was deleted
// after PR merge, it falls back to closed/merged PRs matching that head branch name.
func (c *Client) ListRemoteCommits(ctx context.Context, project, branch string, authors []string) ([]components.RemoteCommit, error) {
	owner, name, ok := splitRepo(project)
	if !ok || strings.TrimSpace(branch) == "" {
		return nil, fmt.Errorf("GitHub 仓库路径（owner/repo）与分支名不能为空")
	}
	branch = strings.TrimSpace(branch)
	q := url.Values{}
	q.Set("sha", branch)
	q.Set("per_page", "50")

	type rawCommit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
				Date  string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
	}

	var raw []rawCommit
	commitsPath := fmt.Sprintf("/repos/%s/%s/commits", url.PathEscape(owner), url.PathEscape(name))
	err := c.getJSON(ctx, commitsPath, q, &raw)
	if err != nil || len(raw) == 0 {
		// Fallback: branch may have been deleted after PR merge -> find closed PRs with head ref == branch
		prQ := url.Values{}
		prQ.Set("state", "closed")
		prQ.Set("per_page", "30")
		var prs []struct {
			Number int `json:"number"`
			Head   struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		prPath := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(owner), url.PathEscape(name))
		if prErr := c.getJSON(ctx, prPath, prQ, &prs); prErr == nil {
			for _, pr := range prs {
				if pr.Head.Ref != branch {
					continue
				}
				var prCommits []rawCommit
				prCommitPath := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits", url.PathEscape(owner), url.PathEscape(name), pr.Number)
				if cErr := c.getJSON(ctx, prCommitPath, nil, &prCommits); cErr == nil && len(prCommits) > 0 {
					raw = prCommits
					err = nil
					break
				}
			}
		}
		if err != nil && len(raw) == 0 {
			return nil, err
		}
	}

	aliases := normalizeAliases(authors)
	out := make([]components.RemoteCommit, 0, len(raw))
	for _, rc := range raw {
		login := ""
		if rc.Author != nil {
			login = rc.Author.Login
		}
		if len(aliases) > 0 && !matchesAuthor(rc.Commit.Author.Name, rc.Commit.Author.Email, login, aliases) {
			continue
		}
		title := strings.SplitN(strings.TrimSpace(rc.Commit.Message), "\n", 2)[0]
		out = append(out, components.RemoteCommit{
			SHA:         rc.SHA,
			Title:       title,
			Author:      rc.Commit.Author.Name,
			CommittedAt: rc.Commit.Author.Date,
			WebURL:      rc.HTMLURL,
		})
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, apiPath string, query url.Values, dst any) error {
	u := c.cfg.BaseURL + apiPath
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Lagrangian/0.2")
	if c.cfg.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Secret)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub 网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("GitHub 认证或权限失败 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitHub 返回错误 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	return nil
}

func splitRepo(repo string) (string, string, bool) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(repo), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func extractRepoFromAPIURL(repoURL string) string {
	idx := strings.Index(repoURL, "/repos/")
	if idx < 0 {
		return ""
	}
	return strings.Trim(repoURL[idx+len("/repos/"):], "/")
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

func matchesAuthor(name, email, login string, aliases []string) bool {
	ln := strings.ToLower(strings.TrimSpace(name))
	le := strings.ToLower(strings.TrimSpace(email))
	ll := strings.ToLower(strings.TrimSpace(login))
	for _, a := range aliases {
		if ln == a || le == a || ll == a || strings.Contains(le, a) {
			return true
		}
	}
	return false
}
