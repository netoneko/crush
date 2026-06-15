package agent

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

const ListRolesToolName = config.ListRolesToolName

const listRolesDescription = `List the specialized sub-agent roles you can delegate to. ` +
	`Each role is a named prompt that tailors the agent tool for a kind of work. ` +
	`Pick the role that best fits the task, then call the agent tool with that role name in its 'role' argument. ` +
	`If no role fits, call the agent tool without a role for the default agent.`

// ListRolesParams is intentionally empty: the tool takes no arguments.
type ListRolesParams struct{}

// listRolesTool returns the catalog of configured subagent_roles so the model
// can discover which role names it may pass to the agent tool. It is only
// granted to the coder when at least one role is configured (see SetupAgents),
// so an empty catalog should not normally be reachable.
func (c *coordinator) listRolesTool() fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ListRolesToolName,
		listRolesDescription,
		func(ctx context.Context, _ ListRolesParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			roles := subagentRoles(c.cfg.Config().Options)
			if len(roles) == 0 {
				return fantasy.NewTextResponse("No roles are configured."), nil
			}
			var b strings.Builder
			b.WriteString("Available roles (pass one as the agent tool's `role` argument):\n")
			for _, name := range sortedRoleNames(roles) {
				summary := c.roleSummary(roles[name])
				if summary == "" {
					fmt.Fprintf(&b, "- %s\n", name)
					continue
				}
				fmt.Fprintf(&b, "- %s: %s\n", name, summary)
			}
			return fantasy.NewTextResponse(b.String()), nil
		},
	)
}

// roleSummary returns a one-line description of a role, taken from the first
// non-blank line of its first prompt file. Returns "" if the file is missing or
// has no usable line, in which case only the role name is shown.
func (c *coordinator) roleSummary(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	content, err := prompt.ConcatPromptFiles(paths[:1], c.cfg)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "# "))
		if line != "" {
			return truncateLine(line, 120)
		}
	}
	return ""
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
