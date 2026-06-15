package agent

import (
	"context"
	_ "embed"
	"slices"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/coder.md.tpl
var coderPromptTmpl []byte

//go:embed templates/coder_compact.md.tpl
var coderCompactPromptTmpl []byte

//go:embed templates/task.md.tpl
var taskPromptTmpl []byte

//go:embed templates/initialize.md.tpl
var initializePromptTmpl []byte

func coderPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("coder", string(coderPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func coderCompactPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("coder", string(coderCompactPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

// resolveSubagentPromptPaths returns the prompt files the spawned Task sub-agent
// should use, or nil to fall back to the built-in task template. The sub-agent's
// own subagent_prompt_paths wins; when it is unset, the sub-agent inherits the
// coder's prompt_paths so an execution-engine override applies end-to-end without
// having to repeat it. Set subagent_prompt_paths to give the sub-agent a
// different prompt from the coder.
func resolveSubagentPromptPaths(o *config.Options) []string {
	if o == nil {
		return nil
	}
	if len(o.SubagentPromptPaths) > 0 {
		return o.SubagentPromptPaths
	}
	return o.PromptPaths
}

// subagentRoles returns the configured role → prompt-files map, or nil. Roles
// let the coder spawn a specialized Task sub-agent by passing a `role` argument
// to the agent tool; see Options.SubagentRoles and the list_roles tool.
func subagentRoles(o *config.Options) map[string][]string {
	if o == nil {
		return nil
	}
	return o.SubagentRoles
}

// sortedRoleNames returns the role names in deterministic order, for stable
// error messages and tool output.
func sortedRoleNames(roles map[string][]string) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// buildSubAgentFor builds a Task sub-agent whose system prompt comes from the
// given prompt files. When paths is empty it falls back to the built-in task
// template. Shared by the default sub-agent and each configured role.
func (c *coordinator) buildSubAgentFor(ctx context.Context, agentCfg config.Agent, paths []string) (SessionAgent, error) {
	promptFn := taskPrompt
	if len(paths) > 0 {
		promptFn = func(po ...prompt.Option) (*prompt.Prompt, error) {
			return promptFromFiles("task", paths, c.cfg, po...)
		}
	}
	sysPrompt, err := promptFn(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}
	return c.buildAgent(ctx, sysPrompt, agentCfg, true)
}

// promptFromFiles builds a system prompt from a user-supplied set of files
// (concatenated in order) instead of the built-in template. The result is still
// rendered as a Go text/template, and the auto-discovered context files are
// suppressed so the injected files are the entire prompt. name is the template
// name ("coder" or "task"). See Options.PromptPaths / Options.SubagentPromptPaths.
func promptFromFiles(name string, paths []string, store *config.ConfigStore, opts ...prompt.Option) (*prompt.Prompt, error) {
	tmpl, err := prompt.ConcatPromptFiles(paths, store)
	if err != nil {
		return nil, err
	}
	opts = append([]prompt.Option{prompt.WithoutContextFiles()}, opts...)
	return prompt.NewPrompt(name, tmpl, opts...)
}

func taskPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("task", string(taskPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func InitializePrompt(cfg *config.ConfigStore) (string, error) {
	systemPrompt, err := prompt.NewPrompt("initialize", string(initializePromptTmpl))
	if err != nil {
		return "", err
	}
	return systemPrompt.Build(context.Background(), "", "", cfg)
}
