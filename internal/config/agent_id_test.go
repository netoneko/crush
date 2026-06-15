package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_AgentIDs(t *testing.T) {
	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{},
		},
	}
	cfg.SetupAgents()

	t.Run("Coder agent should have correct ID", func(t *testing.T) {
		coderAgent, ok := cfg.Agents[AgentCoder]
		require.True(t, ok)
		assert.Equal(t, AgentCoder, coderAgent.ID, "Coder agent ID should be '%s'", AgentCoder)
	})

	t.Run("Task agent should have correct ID", func(t *testing.T) {
		taskAgent, ok := cfg.Agents[AgentTask]
		require.True(t, ok)
		assert.Equal(t, AgentTask, taskAgent.ID, "Task agent ID should be '%s'", AgentTask)
	})
}

func TestSetupAgents_SubagentSelfAssessment(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	t.Run("unset by default so sub-agent inherits global", func(t *testing.T) {
		cfg := &Config{Options: &Options{}}
		cfg.SetupAgents()

		taskAgent := cfg.Agents[AgentTask]
		assert.Nil(t, taskAgent.EnableTaskSelfAssessment, "no override means inherit global")
		assert.Nil(t, taskAgent.TaskSelfAssessment)

		// The coder agent is always left unset so it resolves against global Options.
		coderAgent := cfg.Agents[AgentCoder]
		assert.Nil(t, coderAgent.EnableTaskSelfAssessment)
		assert.Nil(t, coderAgent.TaskSelfAssessment)
	})

	t.Run("sub-agent overrides flow from Options onto the Task agent", func(t *testing.T) {
		tuning := &TaskSelfAssessmentConfig{MaxReminders: 4, TargetCompletion: 0.75}
		cfg := &Config{Options: &Options{
			SubagentEnableTaskSelfAssessment: boolPtr(true),
			SubagentTaskSelfAssessment:       tuning,
			// A global value that the coder uses but the sub-agent override shadows.
			EnableTaskSelfAssessment: boolPtr(false),
		}}
		cfg.SetupAgents()

		taskAgent := cfg.Agents[AgentTask]
		require.NotNil(t, taskAgent.EnableTaskSelfAssessment)
		assert.True(t, *taskAgent.EnableTaskSelfAssessment)
		assert.Same(t, tuning, taskAgent.TaskSelfAssessment)

		// The coder agent is unaffected by the sub-agent override.
		coderAgent := cfg.Agents[AgentCoder]
		assert.Nil(t, coderAgent.EnableTaskSelfAssessment)
		assert.Nil(t, coderAgent.TaskSelfAssessment)
	})
}
