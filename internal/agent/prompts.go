package agent

import (
	"context"
	_ "embed"

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

// coderPromptFromFiles builds the coder system prompt from a user-supplied set of
// files (concatenated in order) instead of the built-in template. The result is
// still rendered as a Go text/template, and the auto-discovered context files are
// suppressed so the injected files are the entire prompt. See Options.PromptPaths.
func coderPromptFromFiles(paths []string, store *config.ConfigStore, opts ...prompt.Option) (*prompt.Prompt, error) {
	tmpl, err := prompt.ConcatPromptFiles(paths, store)
	if err != nil {
		return nil, err
	}
	opts = append([]prompt.Option{prompt.WithoutContextFiles()}, opts...)
	return prompt.NewPrompt("coder", tmpl, opts...)
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
