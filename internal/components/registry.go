package components

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lagrangian-dev/lagrangian/internal/config"
)

// Registry holds all active component instances and exposes a unified snapshot
// for health checks, settings inspection, and the Lagrangian Orbit view.
type Registry struct {
	mu         sync.RWMutex
	components map[string]Component
	slots      []Status
}

func NewRegistry() *Registry {
	return &Registry{
		components: map[string]Component{},
	}
}

func (r *Registry) Register(c Component) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.components[c.ID()] = c
}

func (r *Registry) Get(id string) (Component, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.components[id]
	return c, ok
}

// SyncFromConfig builds declarative component slot statuses from config.yaml so
// configured or planned extension points are always visible and auditable.
func (r *Registry) SyncFromConfig(cfg config.Config) {
	now := time.Now().UTC()
	var list []Status

	appendSlots := func(kind Kind, defaultName string, slots []config.ComponentSlot) {
		if len(slots) == 0 {
			list = append(list, Status{
				ID:        string(kind) + ":unconfigured",
				Kind:      kind,
				Driver:    "none",
				Name:      defaultName,
				Enabled:   false,
				State:     HealthDisabled,
				Detail:    "尚未配置组件实例",
				CheckedAt: now,
			})
			return
		}
		for _, slot := range slots {
			state := HealthDisabled
			detail := "已配置但未启用"
			if slot.Enabled {
				state = HealthPlanned
				detail = "配置已加载，等待对应阶段适配器激活 (" + slot.Driver + ")"
			}
			list = append(list, Status{
				ID:        slot.ID,
				Kind:      kind,
				Driver:    slot.Driver,
				Name:      slot.Name,
				Enabled:   slot.Enabled,
				State:     state,
				Detail:    detail,
				CheckedAt: now,
			})
		}
	}

	if len(cfg.Components.CodeHosts) == 0 {
		reconcileText := "关闭"
		if cfg.Git.AutoReconcileRebase {
			reconcileText = "开启"
		}
		detail := fmt.Sprintf("本地 Git 已就绪（作者别名 %d 项，Rebase 归并%s）；支持接入多实例 GitLab / GitHub", len(cfg.Git.AuthorAliases), reconcileText)
		list = append(list, Status{
			ID:        string(KindCodeHost) + ":local-git",
			Kind:      KindCodeHost,
			Driver:    "local_git",
			Name:      "Git 与代码托管",
			Enabled:   true,
			State:     HealthReady,
			Detail:    detail,
			CheckedAt: now,
		})
	} else {
		appendSlots(KindCodeHost, "Git 与代码托管", cfg.Components.CodeHosts)
	}
	appendSlots(KindIssueTracker, "工单系统 (Jira)", cfg.Components.IssueTrackers)
	appendSlots(KindBotChannel, "多机器人通道 (企微等)", cfg.Components.Bots)
	appendSlots(KindAgentSource, "Agent 对话同步 (多工作节点)", cfg.Components.AgentSources)
	appendSlots(KindLLMProvider, "LLM 模型路由 (本地 / 远端)", cfg.Components.LLMProviders)
	appendSlots(KindKnowledgeBase, "通用知识库 (本地 / 远端)", cfg.Components.KnowledgeBases)

	mcpState := HealthDisabled
	mcpDetail := "未启用 MCP 网关"
	if cfg.Components.MCP.Enabled {
		mcpState = HealthPlanned
		mcpDetail = "MCP 配置已启用 (" + cfg.Components.MCP.Transport + ")，将在 Phase 3 激活"
	}
	list = append(list, Status{
		ID:        "mcp:gateway",
		Kind:      KindMCPGateway,
		Driver:    cfg.Components.MCP.Transport,
		Name:      "通用 MCP 网关",
		Enabled:   cfg.Components.MCP.Enabled,
		State:     mcpState,
		Detail:    mcpDetail,
		CheckedAt: now,
	})

	r.mu.Lock()
	r.slots = list
	r.mu.Unlock()
}

// Snapshot returns live health for registered components plus declarative slot states.
func (r *Registry) Snapshot(ctx context.Context) []Status {
	r.mu.RLock()
	registered := make([]Component, 0, len(r.components))
	for _, c := range r.components {
		registered = append(registered, c)
	}
	slots := make([]Status, len(r.slots))
	copy(slots, r.slots)
	r.mu.RUnlock()

	byID := map[string]Status{}
	for _, s := range slots {
		byID[s.ID] = s
	}
	for _, c := range registered {
		byID[c.ID()] = c.Health(ctx)
	}

	out := make([]Status, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}
