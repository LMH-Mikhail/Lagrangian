// Package jira implements the components.IssueTracker adapter for Jira
// Data Center / Server (REST API v2) and Jira Cloud without hardcoding any
// organization domain, project key, or localized status names.
package jira

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/components"
)

var issueKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-[0-9]+$`)

type Config struct {
	ID       string
	Name     string
	BaseURL  string
	AuthType string // "pat" | "basic"
	Username string
	Secret   string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) ID() string            { return c.cfg.ID }
func (c *Client) Kind() components.Kind { return components.KindIssueTracker }
func (c *Client) Driver() string        { return "jira" }
func (c *Client) Name() string          { return c.cfg.Name }

func (c *Client) Health(ctx context.Context) components.Status {
	who, err := c.TestConnection(ctx)
	now := time.Now().UTC()
	if err != nil {
		return components.Status{
			ID:        c.cfg.ID,
			Kind:      components.KindIssueTracker,
			Driver:    "jira",
			Name:      c.cfg.Name,
			Enabled:   true,
			State:     components.HealthDegraded,
			Detail:    err.Error(),
			CheckedAt: now,
		}
	}
	return components.Status{
		ID:        c.cfg.ID,
		Kind:      components.KindIssueTracker,
		Driver:    "jira",
		Name:      c.cfg.Name,
		Enabled:   true,
		State:     components.HealthReady,
		Detail:    fmt.Sprintf("已连接 Jira (%s)", who),
		CheckedAt: now,
	}
}

// TestConnection verifies credentials against GET /rest/api/2/myself.
func (c *Client) TestConnection(ctx context.Context) (string, error) {
	var me struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
		Email       string `json:"emailAddress"`
	}
	if err := c.getJSON(ctx, "/rest/api/2/myself", nil, &me); err != nil {
		return "", err
	}
	switch {
	case me.DisplayName != "":
		return me.DisplayName, nil
	case me.Name != "":
		return me.Name, nil
	case me.Email != "":
		return me.Email, nil
	default:
		return "authenticated", nil
	}
}

// ListStatuses queries GET /rest/api/2/status to discover the actual localized
// status names configured on the target Jira instance.
func (c *Client) ListStatuses(ctx context.Context) ([]string, error) {
	var raw []struct {
		Name string `json:"name"`
	}
	if err := c.getJSON(ctx, "/rest/api/2/status", nil, &raw); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out, nil
}

// SearchIssues builds a safe JQL query from configurable project, status whitelist,
// assignee ("currentUser()" supported), and keyword / exact key.
func (c *Client) SearchIssues(ctx context.Context, query components.IssueQuery) ([]components.IssueItem, error) {
	jql := BuildJQL(query)
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("maxResults", "50")
	q.Set("fields", "summary,status,assignee,updated")

	var resp struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
				Status  struct {
					Name string `json:"name"`
				} `json:"status"`
				Assignee *struct {
					DisplayName string `json:"displayName"`
					Name        string `json:"name"`
				} `json:"assignee"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := c.getJSON(ctx, "/rest/api/2/search", q, &resp); err != nil {
		return nil, err
	}

	items := make([]components.IssueItem, 0, len(resp.Issues))
	for _, it := range resp.Issues {
		assignee := ""
		if it.Fields.Assignee != nil {
			assignee = it.Fields.Assignee.DisplayName
			if assignee == "" {
				assignee = it.Fields.Assignee.Name
			}
		}
		items = append(items, components.IssueItem{
			Key:      it.Key,
			Title:    it.Fields.Summary,
			Status:   it.Fields.Status.Name,
			Assignee: assignee,
			WebURL:   c.cfg.BaseURL + "/browse/" + it.Key,
		})
	}
	return items, nil
}

// BuildJQL constructs a deterministic JQL expression from structured query filters.
func BuildJQL(query components.IssueQuery) string {
	var clauses []string
	if proj := strings.TrimSpace(query.ProjectKey); proj != "" {
		clauses = append(clauses, fmt.Sprintf("project = %s", quoteJQL(proj)))
	}
	if len(query.Statuses) > 0 {
		quoted := make([]string, 0, len(query.Statuses))
		for _, st := range query.Statuses {
			if t := strings.TrimSpace(st); t != "" {
				quoted = append(quoted, quoteJQL(t))
			}
		}
		if len(quoted) > 0 {
			clauses = append(clauses, "status in ("+strings.Join(quoted, ", ")+")")
		}
	}
	if assignee := strings.TrimSpace(query.Assignee); assignee != "" {
		if strings.EqualFold(assignee, "currentUser()") || strings.EqualFold(assignee, "me") {
			clauses = append(clauses, "assignee = currentUser()")
		} else {
			clauses = append(clauses, fmt.Sprintf("assignee = %s", quoteJQL(assignee)))
		}
	}
	if kw := strings.TrimSpace(query.Keyword); kw != "" {
		if issueKeyPattern.MatchString(kw) {
			clauses = append(clauses, fmt.Sprintf("key = %s", quoteJQL(strings.ToUpper(kw))))
		} else {
			clauses = append(clauses, fmt.Sprintf("summary ~ %s", quoteJQL(kw)))
		}
	}
	if len(clauses) == 0 {
		return "ORDER BY updated DESC"
	}
	return strings.Join(clauses, " AND ") + " ORDER BY updated DESC"
}

func quoteJQL(v string) string {
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func (c *Client) getJSON(ctx context.Context, apiPath string, query url.Values, dst any) error {
	if c.cfg.BaseURL == "" {
		return fmt.Errorf("Jira base_url 未配置")
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
		if strings.EqualFold(c.cfg.AuthType, "basic") {
			cred := base64.StdEncoding.EncodeToString([]byte(c.cfg.Username + ":" + c.cfg.Secret))
			req.Header.Set("Authorization", "Basic "+cred)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Secret)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Jira 网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("Jira 认证失败 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Jira 返回错误 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("解析 Jira 响应失败: %w", err)
	}
	return nil
}
