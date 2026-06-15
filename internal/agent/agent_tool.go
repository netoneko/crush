package agent

import (
	"context"
	_ "embed"
	"errors"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

type AgentParams struct {
	Prompt string `json:"prompt" description:"The task for the agent to perform"`
}

const (
	AgentToolName = "agent"
)

func (c *coordinator) agentTool(ctx context.Context) (fantasy.AgentTool, error) {
	agentCfg, ok := c.cfg.Config().Agents[config.AgentTask]
	if !ok {
		return nil, errors.New("task agent not configured")
	}
	// Resolve the sub-agent prompt: subagent_prompt_paths wins, else it inherits
	// the coder's prompt_paths, else the built-in task template.
	promptFn := taskPrompt
	if paths := resolveSubagentPromptPaths(c.cfg.Config().Options); len(paths) > 0 {
		promptFn = func(po ...prompt.Option) (*prompt.Prompt, error) {
			return promptFromFiles("task", paths, c.cfg, po...)
		}
	}
	sysPrompt, err := promptFn(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}

	agent, err := c.buildAgent(ctx, sysPrompt, agentCfg, true)
	if err != nil {
		return nil, err
	}
	return fantasy.NewParallelAgentTool(
		AgentToolName,
		agentToolDescription,
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
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
