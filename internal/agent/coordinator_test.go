package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSessionAgent is a minimal mock for the SessionAgent interface.
type mockSessionAgent struct {
	model     Model
	runFunc   func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error)
	cancelled []string
}

func (m *mockSessionAgent) Run(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
	return m.runFunc(ctx, call)
}

func (m *mockSessionAgent) Model() Model                        { return m.model }
func (m *mockSessionAgent) SetModels(large, small Model)        {}
func (m *mockSessionAgent) SetTools(tools []fantasy.AgentTool)  {}
func (m *mockSessionAgent) SetSystemPrompt(systemPrompt string) {}
func (m *mockSessionAgent) Cancel(sessionID string) {
	m.cancelled = append(m.cancelled, sessionID)
}
func (m *mockSessionAgent) CancelAll()                                  {}
func (m *mockSessionAgent) IsSessionBusy(sessionID string) bool         { return false }
func (m *mockSessionAgent) IsBusy() bool                                { return false }
func (m *mockSessionAgent) QueuedPrompts(sessionID string) int          { return 0 }
func (m *mockSessionAgent) QueuedPromptsList(sessionID string) []string { return nil }
func (m *mockSessionAgent) ClearQueue(sessionID string)                 {}
func (m *mockSessionAgent) Summarize(context.Context, string, fantasy.ProviderOptions) error {
	return nil
}

// newTestCoordinator creates a minimal coordinator for unit testing runSubAgent.
func newTestCoordinator(t *testing.T, env fakeEnv, providerID string, providerCfg config.ProviderConfig) *coordinator {
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.Config().Providers.Set(providerID, providerCfg)
	return &coordinator{
		cfg:      cfg,
		sessions: env.sessions,
	}
}

// newMockAgent creates a mockSessionAgent with the given provider and run function.
func newMockAgent(providerID string, maxTokens int64, runFunc func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)) *mockSessionAgent {
	return &mockSessionAgent{
		model: Model{
			CatwalkCfg: catwalk.Model{
				DefaultMaxTokens: maxTokens,
			},
			ModelCfg: config.SelectedModel{
				Provider: providerID,
			},
		},
		runFunc: runFunc,
	}
}

// agentResultWithText creates a minimal AgentResult with the given text response.
func agentResultWithText(text string) *fantasy.AgentResult {
	return &fantasy.AgentResult{
		Response: fantasy.Response{
			Content: fantasy.ResponseContent{
				fantasy.TextContent{Text: text},
			},
		},
	}
}

func TestRunSubAgent(t *testing.T) {
	const providerID = "test-provider"
	providerCfg := config.ProviderConfig{ID: providerID}

	t.Run("happy path", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			assert.Equal(t, "do something", call.Prompt)
			assert.Equal(t, int64(4096), call.MaxOutputTokens)
			return agentResultWithText("done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "do something",
			SessionTitle:   "Test Session",
		})
		require.NoError(t, err)
		assert.Equal(t, "done", resp.Content)
		assert.False(t, resp.IsError)
	})

	t.Run("ModelCfg.MaxTokens overrides default", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := &mockSessionAgent{
			model: Model{
				CatwalkCfg: catwalk.Model{
					DefaultMaxTokens: 4096,
				},
				ModelCfg: config.SelectedModel{
					Provider:  providerID,
					MaxTokens: 8192,
				},
			},
			runFunc: func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				assert.Equal(t, int64(8192), call.MaxOutputTokens)
				return agentResultWithText("ok"), nil
			},
		}

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Content)
	})

	t.Run("session creation failure with canceled context", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, nil)

		// Use a canceled context to trigger CreateTaskSession failure.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = coord.runSubAgent(ctx, subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
	})

	t.Run("provider not configured", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		// Agent references a provider that doesn't exist in config.
		agent := newMockAgent("unknown-provider", 4096, nil)

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "model provider not configured")
	})

	t.Run("agent run error returns error response", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return nil, errors.New("provider request failed")
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		// runSubAgent returns (errorResponse, nil) when agent.Run fails — not a Go error.
		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Equal(t, "Failed to generate response: provider request failed", resp.Content)
	})

	t.Run("session setup callback is invoked", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		var setupCalledWith string
		agent := newMockAgent(providerID, 4096, func(_ context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
			SessionSetup: func(sessionID string) {
				setupCalledWith = sessionID
			},
		})
		require.NoError(t, err)
		assert.NotEmpty(t, setupCalledWith, "SessionSetup should have been called")
	})

	t.Run("cost propagation to parent session", func(t *testing.T) {
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, providerCfg)

		parentSession, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
			// Simulate the agent incurring cost by updating the child session.
			childSession, err := env.sessions.Get(ctx, call.SessionID)
			if err != nil {
				return nil, err
			}
			childSession.Cost = 0.05
			_, err = env.sessions.Save(ctx, childSession)
			if err != nil {
				return nil, err
			}
			return agentResultWithText("ok"), nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent:          agent,
			SessionID:      parentSession.ID,
			AgentMessageID: "msg-1",
			ToolCallID:     "call-1",
			Prompt:         "test",
			SessionTitle:   "Test",
		})
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parentSession.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.05, updated.Cost, 1e-9)
	})
}

func TestUpdateParentSessionCost(t *testing.T) {
	t.Run("accumulates cost correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		// Set child cost.
		child.Cost = 0.10
		_, err = env.sessions.Save(t.Context(), child)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.10, updated.Cost, 1e-9)
	})

	t.Run("accumulates multiple child costs", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		child1, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child1")
		require.NoError(t, err)
		child1.Cost = 0.05
		_, err = env.sessions.Save(t.Context(), child1)
		require.NoError(t, err)

		child2, err := env.sessions.CreateTaskSession(t.Context(), "tool-2", parent.ID, "Child2")
		require.NoError(t, err)
		child2.Cost = 0.03
		_, err = env.sessions.Save(t.Context(), child2)
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child1.ID, parent.ID)
		require.NoError(t, err)
		err = coord.updateParentSessionCost(t.Context(), child2.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.08, updated.Cost, 1e-9)
	})

	t.Run("child session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), "non-existent", parent.ID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get child session")
	})

	t.Run("parent session not found", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, "non-existent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "get parent session")
	})

	t.Run("zero cost handled correctly", func(t *testing.T) {
		env := testEnv(t)
		cfg, err := config.Init(env.workingDir, "", false)
		require.NoError(t, err)
		coord := &coordinator{cfg: cfg, sessions: env.sessions}

		parent, err := env.sessions.Create(t.Context(), "Parent")
		require.NoError(t, err)
		child, err := env.sessions.CreateTaskSession(t.Context(), "tool-1", parent.ID, "Child")
		require.NoError(t, err)

		err = coord.updateParentSessionCost(t.Context(), child.ID, parent.ID)
		require.NoError(t, err)

		updated, err := env.sessions.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.InDelta(t, 0.0, updated.Cost, 1e-9)
	})
}

func TestResolveTaskAssessmentSettings(t *testing.T) {
	tests := []struct {
		name          string
		cfg           *config.TaskSelfAssessmentConfig
		wantReminders int
		wantTarget    float64
	}{
		{"nil config uses defaults", nil, 1, 1.0},
		{"empty config uses defaults", &config.TaskSelfAssessmentConfig{}, 1, 1.0},
		{"max reminders honored", &config.TaskSelfAssessmentConfig{MaxReminders: 5}, 5, 1.0},
		{"target honored", &config.TaskSelfAssessmentConfig{TargetCompletion: 0.5}, 1, 0.5},
		{"both honored", &config.TaskSelfAssessmentConfig{MaxReminders: 3, TargetCompletion: 0.8}, 3, 0.8},
		{"zero max falls back", &config.TaskSelfAssessmentConfig{MaxReminders: 0}, 1, 1.0},
		{"negative max falls back", &config.TaskSelfAssessmentConfig{MaxReminders: -2}, 1, 1.0},
		{"out-of-range target falls back", &config.TaskSelfAssessmentConfig{TargetCompletion: 1.5}, 1, 1.0},
		{"zero target falls back", &config.TaskSelfAssessmentConfig{TargetCompletion: 0}, 1, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotReminders, gotTarget := resolveTaskAssessmentSettings(tc.cfg)
			assert.Equal(t, tc.wantReminders, gotReminders)
			assert.InDelta(t, tc.wantTarget, gotTarget, 1e-9)
		})
	}
}

func TestBuildTaskAssessmentPrompt(t *testing.T) {
	t.Run("empty list returns false", func(t *testing.T) {
		_, ok := buildTaskAssessmentPrompt(nil)
		assert.False(t, ok)
	})

	t.Run("all completed returns false", func(t *testing.T) {
		_, ok := buildTaskAssessmentPrompt([]session.Todo{
			{Content: "a", Status: session.TodoStatusCompleted},
		})
		assert.False(t, ok)
	})

	t.Run("lists unfinished tasks by name", func(t *testing.T) {
		prompt, ok := buildTaskAssessmentPrompt([]session.Todo{
			{Content: "Wire up the parser", Status: session.TodoStatusCompleted},
			{Content: "Add the CLI flag", Status: session.TodoStatusPending},
			{Content: "Write the docs", Status: session.TodoStatusInProgress},
		})
		require.True(t, ok)
		// Full list (with statuses) is present so the model can submit a
		// non-clobbering full replacement.
		assert.Contains(t, prompt, "[completed] Wire up the parser")
		// Unfinished tasks are also called out by name in their own section.
		assert.Contains(t, prompt, "Still unfinished:")
		assert.Contains(t, prompt, "- Add the CLI flag")
		assert.Contains(t, prompt, "- Write the docs")
		// A completed task must not appear in the unfinished section.
		assert.NotContains(t, prompt, "- Wire up the parser\n")
	})
}

// completeOneTodoPerCall returns a runFunc that marks the first incomplete
// todo of the session as completed on each invocation, simulating a model
// that closes out one task per reminder. It records each prompt it sees.
func completeOneTodoPerCall(t *testing.T, sessions session.Service, prompts *[]string) func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
	t.Helper()
	return func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		*prompts = append(*prompts, call.Prompt)
		sess, err := sessions.Get(ctx, call.SessionID)
		require.NoError(t, err)
		for i := range sess.Todos {
			if sess.Todos[i].Status != session.TodoStatusCompleted {
				sess.Todos[i].Status = session.TodoStatusCompleted
				break
			}
		}
		_, err = sessions.Save(ctx, sess)
		require.NoError(t, err)
		return agentResultWithText("ok"), nil
	}
}

func newSessionWithTodos(t *testing.T, sessions session.Service, n int) session.Session {
	t.Helper()
	sess, err := sessions.Create(t.Context(), "Test")
	require.NoError(t, err)
	sess.Todos = make([]session.Todo, n)
	for i := range sess.Todos {
		sess.Todos[i] = session.Todo{
			Content: fmt.Sprintf("task-%d", i),
			Status:  session.TodoStatusPending,
		}
	}
	saved, err := sessions.Save(t.Context(), sess)
	require.NoError(t, err)
	return saved
}

func coordWithAssessment(t *testing.T, env fakeEnv, cfg *config.TaskSelfAssessmentConfig) *coordinator {
	t.Helper()
	conf, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	conf.Config().Options.TaskSelfAssessment = cfg
	return &coordinator{cfg: conf, sessions: env.sessions}
}

func TestRunTaskSelfAssessment(t *testing.T) {
	t.Run("loops until all todos are closed", func(t *testing.T) {
		env := testEnv(t)
		coord := coordWithAssessment(t, env, &config.TaskSelfAssessmentConfig{MaxReminders: 10})
		sess := newSessionWithTodos(t, env.sessions, 3)

		var prompts []string
		agent := newMockAgent("p", 4096, completeOneTodoPerCall(t, env.sessions, &prompts))

		sent := coord.runTaskSelfAssessment(t.Context(), agent, SessionAgentCall{SessionID: sess.ID})
		assert.Equal(t, 3, sent, "one reminder per incomplete task")
		assert.Len(t, prompts, 3)
		assert.Contains(t, prompts[0], "task-0")

		final, err := env.sessions.Get(t.Context(), sess.ID)
		require.NoError(t, err)
		assert.False(t, session.HasIncompleteTodos(final.Todos))
	})

	t.Run("respects max-reminders cap", func(t *testing.T) {
		env := testEnv(t)
		coord := coordWithAssessment(t, env, &config.TaskSelfAssessmentConfig{MaxReminders: 2})
		sess := newSessionWithTodos(t, env.sessions, 5)

		var prompts []string
		agent := newMockAgent("p", 4096, completeOneTodoPerCall(t, env.sessions, &prompts))

		sent := coord.runTaskSelfAssessment(t.Context(), agent, SessionAgentCall{SessionID: sess.ID})
		assert.Equal(t, 2, sent)

		final, err := env.sessions.Get(t.Context(), sess.ID)
		require.NoError(t, err)
		assert.True(t, session.HasIncompleteTodos(final.Todos), "cap stops before all are closed")
	})

	t.Run("stops once target completion is reached", func(t *testing.T) {
		env := testEnv(t)
		coord := coordWithAssessment(t, env, &config.TaskSelfAssessmentConfig{MaxReminders: 10, TargetCompletion: 0.5})
		sess := newSessionWithTodos(t, env.sessions, 4)

		var prompts []string
		agent := newMockAgent("p", 4096, completeOneTodoPerCall(t, env.sessions, &prompts))

		sent := coord.runTaskSelfAssessment(t.Context(), agent, SessionAgentCall{SessionID: sess.ID})
		assert.Equal(t, 2, sent, "stops when 2/4 = 0.5 of tasks are done")
	})

	t.Run("no reminders when all todos already complete", func(t *testing.T) {
		env := testEnv(t)
		coord := coordWithAssessment(t, env, &config.TaskSelfAssessmentConfig{MaxReminders: 10})
		sess, err := env.sessions.Create(t.Context(), "Test")
		require.NoError(t, err)
		sess.Todos = []session.Todo{{Content: "done", Status: session.TodoStatusCompleted}}
		_, err = env.sessions.Save(t.Context(), sess)
		require.NoError(t, err)

		agent := newMockAgent("p", 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
			t.Fatal("agent should not be called when all todos are complete")
			return nil, nil
		})

		sent := coord.runTaskSelfAssessment(t.Context(), agent, SessionAgentCall{SessionID: sess.ID})
		assert.Equal(t, 0, sent)
	})

	t.Run("default config sends a single reminder", func(t *testing.T) {
		env := testEnv(t)
		coord := coordWithAssessment(t, env, nil)
		sess := newSessionWithTodos(t, env.sessions, 3)

		var prompts []string
		agent := newMockAgent("p", 4096, completeOneTodoPerCall(t, env.sessions, &prompts))

		sent := coord.runTaskSelfAssessment(t.Context(), agent, SessionAgentCall{SessionID: sess.ID})
		assert.Equal(t, 1, sent, "historical one-shot behavior without tuning config")
	})

	t.Run("agent error stops the loop", func(t *testing.T) {
		env := testEnv(t)
		coord := coordWithAssessment(t, env, &config.TaskSelfAssessmentConfig{MaxReminders: 10})
		sess := newSessionWithTodos(t, env.sessions, 3)

		calls := 0
		agent := newMockAgent("p", 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
			calls++
			return nil, errors.New("provider boom")
		})

		sent := coord.runTaskSelfAssessment(t.Context(), agent, SessionAgentCall{SessionID: sess.ID})
		assert.Equal(t, 0, sent, "a failed reminder is not counted")
		assert.Equal(t, 1, calls, "loop aborts after the first failure")
	})
}

func TestGetProviderOptionsReasoningEffort(t *testing.T) {
	// Bedrock is Fantasy's Anthropic under a different provider name; options
	// must land under anthropic.Name so the Anthropic language model picks them up.
	tests := []struct {
		name         string
		providerType catwalk.Type
	}{
		{"anthropic honors reasoning_effort", catwalk.Type(anthropic.Name)},
		{"bedrock honors reasoning_effort", catwalk.Type(bedrock.Name)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{
				CatwalkCfg: catwalk.Model{
					ID:              "claude-opus-4-7",
					CanReason:       true,
					ReasoningLevels: []string{"max"},
				},
				ModelCfg: config.SelectedModel{
					Provider:        "test",
					ReasoningEffort: "max",
				},
			}
			providerCfg := config.ProviderConfig{ID: "test", Type: tc.providerType}

			opts := getProviderOptions(model, providerCfg)

			raw, ok := opts[anthropic.Name]
			require.True(t, ok, "options should be keyed under anthropic.Name for type %q", tc.providerType)
			parsed, ok := raw.(*anthropic.ProviderOptions)
			require.True(t, ok)
			require.NotNil(t, parsed.Effort)
			assert.Equal(t, anthropic.Effort("max"), *parsed.Effort)
		})
	}
}
