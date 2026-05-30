package agent

import (
	"context"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy/providers/openaicompat"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptSelection(t *testing.T) {
	env := testEnv(t)
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	t.Run("default uses coder prompt", func(t *testing.T) {
		p, err := coderPrompt(prompt.WithWorkingDir(env.workingDir))
		require.NoError(t, err)
		full, err := p.Build(context.TODO(), "anthropic", "claude-3-5-sonnet-20241022", cfg)
		require.NoError(t, err)
		assert.NotEmpty(t, full)
	})

	t.Run("compact_prompt uses shorter template", func(t *testing.T) {
		full, err := coderPrompt(prompt.WithWorkingDir(env.workingDir))
		require.NoError(t, err)
		fullText, err := full.Build(context.TODO(), "anthropic", "claude-3-5-sonnet-20241022", cfg)
		require.NoError(t, err)

		compact, err := coderCompactPrompt(prompt.WithWorkingDir(env.workingDir))
		require.NoError(t, err)
		compactText, err := compact.Build(context.TODO(), "anthropic", "claude-3-5-sonnet-20241022", cfg)
		require.NoError(t, err)

		assert.Less(t, len(compactText), len(fullText),
			"compact prompt should be shorter than full prompt")
	})
}

type mockPublisher struct {
	published []notify.Notification
}

func (m *mockPublisher) Publish(t pubsub.EventType, payload notify.Notification) {
	m.published = append(m.published, payload)
}

func (m *mockPublisher) PublishMustDeliver(ctx context.Context, t pubsub.EventType, payload notify.Notification) {
	m.published = append(m.published, payload)
}

func TestBuildAgentSkipsSummarizeForSubAgents(t *testing.T) {
	env := testEnv(t)

	// Create a custom coordinator config
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	// Enable summarize_prompt
	cfg.Config().Options.SummarizePrompt = true

	// Configure mock providers/models
	providerID := "test-provider"
	providerCfg := config.ProviderConfig{
		ID:   providerID,
		Type: catwalk.Type(openaicompat.Name),
		Models: []catwalk.Model{
			{ID: "large-model"},
			{ID: "small-model"},
		},
	}
	cfg.Config().Providers.Set(providerID, providerCfg)
	cfg.Config().Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: providerID, Model: "large-model"}
	cfg.Config().Models[config.SelectedModelTypeSmall] = config.SelectedModel{Provider: providerID, Model: "small-model"}

	publisher := &mockPublisher{}

	coord := &coordinator{
		cfg:         cfg,
		sessions:    env.sessions,
		messages:    env.messages,
		permissions: env.permissions,
		history:     env.history,
		filetracker: *env.filetracker,
		notify:      publisher,
	}

	p, err := coderPrompt(prompt.WithWorkingDir(env.workingDir))
	require.NoError(t, err)

	agentConfig := config.Agent{}

	t.Run("subagent does not trigger summarize", func(t *testing.T) {
		// Reset publisher
		publisher.published = nil

		// Call buildAgent with isSubAgent = true
		agent, err := coord.buildAgent(context.TODO(), p, agentConfig, true)
		require.NoError(t, err)
		require.NotNil(t, agent)

		// Wait for readyWg tasks
		err = coord.readyWg.Wait()
		require.NoError(t, err)

		// Assert that summarize was NOT called by checking notifications
		for _, n := range publisher.published {
			assert.NotEqual(t, notify.TypeSystemPromptCompressing, n.Type, "sub-agent should not emit TypeSystemPromptCompressing")
			assert.NotEqual(t, notify.TypeSystemPromptCompressed, n.Type, "sub-agent should not emit TypeSystemPromptCompressed")
		}
	})
}
