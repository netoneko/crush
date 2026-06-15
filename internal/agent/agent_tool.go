package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

type AgentParams struct {
	Prompt string `json:"prompt" description:"The task for the agent to perform"`
	Role   string `json:"role,omitempty" description:"Optional role specializing the agent. Must be one of the configured roles (use the list_roles tool to discover them). Leave empty for the default agent."`
}

const (
	AgentToolName = "agent"
)

func (c *coordinator) agentTool(ctx context.Context) (fantasy.AgentTool, error) {
	agentCfg, ok := c.cfg.Config().Agents[config.AgentTask]
	if !ok {
		return nil, errors.New("task agent not configured")
	}

	// Default sub-agent: subagent_prompt_paths wins, else it inherits the coder's
	// prompt_paths, else the built-in task template.
	defaultAgent, err := c.buildSubAgentFor(ctx, agentCfg, resolveSubagentPromptPaths(c.cfg.Config().Options))
	if err != nil {
		return nil, err
	}

	// Pre-build one sub-agent per configured role. The tool runs calls in
	// parallel, so we cannot mutate a single agent's prompt per call — each role
	// gets its own agent, selected at call time by the `role` argument.
	roles := subagentRoles(c.cfg.Config().Options)
	roleAgents := make(map[string]SessionAgent, len(roles))
	for name, paths := range roles {
		a, err := c.buildSubAgentFor(ctx, agentCfg, paths)
		if err != nil {
			return nil, fmt.Errorf("subagent role %q: %w", name, err)
		}
		roleAgents[name] = a
	}

	return fantasy.NewParallelAgentTool(
		AgentToolName,
		agentToolDescription,
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			agent := defaultAgent
			if params.Role != "" {
				a, ok := roleAgents[params.Role]
				if !ok {
					return fantasy.NewTextErrorResponse(fmt.Sprintf(
						"unknown role %q; available roles: %s", params.Role, strings.Join(sortedRoleNames(roles), ", "))), nil
				}
				agent = a
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          agent,
				AgentCfg:       agentCfg,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				SessionTitle:   "New Agent Session",
				// Inherit the parent's permission posture: if the parent session is
				// auto-approved (e.g. non-interactive `crush run`), auto-approve the
				// sub-session too so its tool/MCP calls don't block on a prompt that
				// has no responder. In interactive mode the parent isn't auto-approved,
				// so the sub-agent's requests still surface to the user.
				SessionSetup: func(subSessionID string) {
					if c.permissions.IsAutoApproved(sessionID) {
						c.permissions.AutoApproveSession(subSessionID)
					}
				},
			})
		},
	), nil
}
