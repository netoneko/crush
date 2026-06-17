package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/oauth"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/invopop/jsonschema"
)

const (
	appName              = "crush"
	defaultDataDirectory = ".crush"
	defaultInitializeAs  = "AGENTS.md"
)

var defaultContextPaths = []string{
	".github/copilot-instructions.md",
	".cursorrules",
	".cursor/rules/",
	"CLAUDE.md",
	"CLAUDE.local.md",
	"GEMINI.md",
	"gemini.md",
	"crush.md",
	"crush.local.md",
	"Crush.md",
	"Crush.local.md",
	"CRUSH.md",
	"CRUSH.local.md",
	"AGENTS.md",
	"agents.md",
	"Agents.md",
}

type SelectedModelType string

// String returns the string representation of the [SelectedModelType].
func (s SelectedModelType) String() string {
	return string(s)
}

const (
	SelectedModelTypeLarge SelectedModelType = "large"
	SelectedModelTypeSmall SelectedModelType = "small"
)

const (
	AgentCoder string = "coder"
	AgentTask  string = "task"
)

// ListRolesToolName is the name of the tool that lists the configured
// subagent_roles. Defined here (not in the agent package) so config can grant
// it to the coder without an import cycle.
const ListRolesToolName = "list_roles"

type SelectedModel struct {
	// The model id as used by the provider API.
	// Required.
	Model string `json:"model" jsonschema:"required,description=The model ID as used by the provider API,example=gpt-4o"`
	// The model provider, same as the key/id used in the providers config.
	// Required.
	Provider string `json:"provider" jsonschema:"required,description=The model provider ID that matches a key in the providers config,example=openai"`

	// Only used by models that use the openai provider and need this set.
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema:"description=Reasoning effort level for OpenAI models that support it,enum=low,enum=medium,enum=high"`

	// Used by anthropic models that can reason to indicate if the model should think.
	Think bool `json:"think,omitempty" jsonschema:"description=Enable thinking mode for Anthropic models that support reasoning"`

	// Overrides the default model configuration.
	MaxTokens        int64    `json:"max_tokens,omitempty" jsonschema:"description=Maximum number of tokens for model responses,maximum=200000,example=4096"`
	Temperature      *float64 `json:"temperature,omitempty" jsonschema:"description=Sampling temperature,minimum=0,maximum=1,example=0.7"`
	TopP             *float64 `json:"top_p,omitempty" jsonschema:"description=Top-p (nucleus) sampling parameter,minimum=0,maximum=1,example=0.9"`
	TopK             *int64   `json:"top_k,omitempty" jsonschema:"description=Top-k sampling parameter"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty" jsonschema:"description=Frequency penalty to reduce repetition"`
	PresencePenalty  *float64 `json:"presence_penalty,omitempty" jsonschema:"description=Presence penalty to increase topic diversity"`

	// Override provider specific options.
	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for the model"`
}

type ProviderConfig struct {
	// The provider's id.
	ID string `json:"id,omitempty" jsonschema:"description=Unique identifier for the provider,example=openai"`
	// The provider's name, used for display purposes.
	Name string `json:"name,omitempty" jsonschema:"description=Human-readable name for the provider,example=OpenAI"`
	// The provider's API endpoint.
	BaseURL string `json:"base_url,omitempty" jsonschema:"description=Base URL for the provider's API,format=uri,example=https://api.openai.com/v1"`
	// The provider type, e.g. "openai", "anthropic", etc. if empty it defaults to openai.
	Type catwalk.Type `json:"type,omitempty" jsonschema:"description=Provider type that determines the API format,enum=openai,enum=openai-compat,enum=anthropic,enum=gemini,enum=azure,enum=vertexai,default=openai"`
	// The provider's API key.
	APIKey string `json:"api_key,omitempty" jsonschema:"description=API key for authentication with the provider,example=$OPENAI_API_KEY"`
	// The original API key template before resolution (for re-resolution on auth errors).
	APIKeyTemplate string `json:"-"`
	// OAuthToken for providers that use OAuth2 authentication.
	OAuthToken *oauth.Token `json:"oauth,omitempty" jsonschema:"description=OAuth2 token for authentication with the provider"`
	// Marks the provider as disabled.
	Disable bool `json:"disable,omitempty" jsonschema:"description=Whether this provider is disabled,default=false"`

	// Custom system prompt prefix.
	SystemPromptPrefix string `json:"system_prompt_prefix,omitempty" jsonschema:"description=Custom prefix to add to system prompts for this provider"`

	// Extra headers to send with each request to the provider. Values
	// run through shell expansion at config-load time, so $VAR and
	// $(cmd) work the same way they do in MCP headers. A header whose
	// value resolves to the empty string (unset bare $VAR under
	// lenient nounset, $(echo), or literal "") is omitted from the
	// outgoing request rather than sent as "Header:".
	ExtraHeaders map[string]string `json:"extra_headers,omitempty" jsonschema:"description=Additional HTTP headers to send with requests"`
	// ExtraBody is merged verbatim into OpenAI-compatible request
	// bodies. String values are NOT shell-expanded: this is a plain
	// JSON passthrough so that arbitrary provider-extension fields
	// (numbers, nested objects, booleans) round-trip without a
	// recursive walker guessing at intent. If you need an env-var-
	// driven value at request time, put it in extra_headers, or in
	// the provider's top-level api_key / base_url, all of which do
	// expand.
	ExtraBody map[string]any `json:"extra_body,omitempty" jsonschema:"description=Additional fields to include in request bodies\\, only works with openai-compatible providers"`

	ProviderOptions map[string]any `json:"provider_options,omitempty" jsonschema:"description=Additional provider-specific options for this provider"`

	// Used to pass extra parameters to the provider.
	ExtraParams map[string]string `json:"-"`

	// Skip cost accumulation for this provider when using subscription or flat rate billing.
	FlatRate bool `json:"flat_rate,omitempty" jsonschema:"description=Flat-rate mode for this provider"`

	// The provider models
	Models []catwalk.Model `json:"models,omitempty" jsonschema:"description=List of models available from this provider"`
}

// ToProvider converts the [ProviderConfig] to a [catwalk.Provider].
func (c *ProviderConfig) ToProvider() catwalk.Provider {
	// Convert config provider to provider.Provider format
	provider := catwalk.Provider{
		Name:   c.Name,
		ID:     catwalk.InferenceProvider(c.ID),
		Models: make([]catwalk.Model, len(c.Models)),
	}

	// Convert models
	for i, model := range c.Models {
		provider.Models[i] = catwalk.Model{
			ID:                     model.ID,
			Name:                   model.Name,
			CostPer1MIn:            model.CostPer1MIn,
			CostPer1MOut:           model.CostPer1MOut,
			CostPer1MInCached:      model.CostPer1MInCached,
			CostPer1MOutCached:     model.CostPer1MOutCached,
			ContextWindow:          model.ContextWindow,
			DefaultMaxTokens:       model.DefaultMaxTokens,
			CanReason:              model.CanReason,
			ReasoningLevels:        model.ReasoningLevels,
			DefaultReasoningEffort: model.DefaultReasoningEffort,
			SupportsImages:         model.SupportsImages,
		}
	}

	return provider
}

func (c *ProviderConfig) SetupGitHubCopilot() {
	maps.Copy(c.ExtraHeaders, copilot.Headers())
}

type MCPType string

const (
	MCPStdio MCPType = "stdio"
	MCPSSE   MCPType = "sse"
	MCPHttp  MCPType = "http"
)

type MCPConfig struct {
	Command       string            `json:"command,omitempty" jsonschema:"description=Command to execute for stdio MCP servers,example=npx"`
	Env           map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set for the MCP server"`
	Args          []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the MCP server command"`
	Type          MCPType           `json:"type" jsonschema:"required,description=Type of MCP connection,enum=stdio,enum=sse,enum=http,default=stdio"`
	URL           string            `json:"url,omitempty" jsonschema:"description=URL for HTTP or SSE MCP servers,format=uri,example=http://localhost:3000/mcp"`
	Disabled      bool              `json:"disabled,omitempty" jsonschema:"description=Whether this MCP server is disabled,default=false"`
	DisabledTools []string          `json:"disabled_tools,omitempty" jsonschema:"description=List of tools from this MCP server to disable,example=get-library-doc"`
	EnabledTools  []string          `json:"enabled_tools,omitempty" jsonschema:"description=Allow list of tools from this MCP server,example=get-library-doc"`
	Timeout       int               `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for MCP server connections,default=15,example=30,example=60,example=120"`

	// Headers are HTTP headers for HTTP/SSE MCP servers. Values run
	// through shell expansion at MCP startup, so $VAR and $(cmd)
	// work. A header whose value resolves to the empty string (unset
	// bare $VAR under lenient nounset, $(echo), or literal "") is
	// omitted from the outgoing request rather than sent as
	// "Header:".
	Headers map[string]string `json:"headers,omitempty" jsonschema:"description=HTTP headers for HTTP/SSE MCP servers"`
}

type LSPConfig struct {
	Disabled    bool              `json:"disabled,omitempty" jsonschema:"description=Whether this LSP server is disabled,default=false"`
	Command     string            `json:"command,omitempty" jsonschema:"description=Command to execute for the LSP server,example=gopls"`
	Args        []string          `json:"args,omitempty" jsonschema:"description=Arguments to pass to the LSP server command"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"description=Environment variables to set to the LSP server command"`
	FileTypes   []string          `json:"filetypes,omitempty" jsonschema:"description=File types this LSP server handles,example=go,example=mod,example=rs,example=c,example=js,example=ts"`
	RootMarkers []string          `json:"root_markers,omitempty" jsonschema:"description=Files or directories that indicate the project root,example=go.mod,example=package.json,example=Cargo.toml"`
	InitOptions map[string]any    `json:"init_options,omitempty" jsonschema:"description=Initialization options passed to the LSP server during initialize request"`
	Options     map[string]any    `json:"options,omitempty" jsonschema:"description=LSP server-specific settings passed during initialization"`
	Timeout     int               `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for LSP server initialization,default=30,example=60,example=120"`
}

type TUIOptions struct {
	CompactMode bool   `json:"compact_mode,omitempty" jsonschema:"description=Enable compact mode for the TUI interface,default=false"`
	DiffMode    string `json:"diff_mode,omitempty" jsonschema:"description=Diff mode for the TUI interface,enum=unified,enum=split"`
	// Here we can add themes later or any TUI related options
	//

	Completions Completions `json:"completions,omitzero" jsonschema:"description=Completions UI options"`
	Transparent *bool       `json:"transparent,omitempty" jsonschema:"description=Enable transparent background for the TUI interface,default=false"`
}

// Completions defines options for the completions UI.
type Completions struct {
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

func (c Completions) Limits() (depth, items int) {
	return ptrValOr(c.MaxDepth, 0), ptrValOr(c.MaxItems, 0)
}

type Permissions struct {
	AllowedTools []string `json:"allowed_tools,omitempty" jsonschema:"description=List of tools that don't require permission prompts,example=bash,example=view"`
}

type TrailerStyle string

const (
	TrailerStyleNone         TrailerStyle = "none"
	TrailerStyleCoAuthoredBy TrailerStyle = "co-authored-by"
	TrailerStyleAssistedBy   TrailerStyle = "assisted-by"
)

type Attribution struct {
	TrailerStyle  TrailerStyle `json:"trailer_style,omitempty" jsonschema:"description=Style of attribution trailer to add to commits,enum=none,enum=co-authored-by,enum=assisted-by,default=assisted-by"`
	CoAuthoredBy  *bool        `json:"co_authored_by,omitempty" jsonschema:"description=Deprecated: use trailer_style instead"`
	GeneratedWith bool         `json:"generated_with,omitempty" jsonschema:"description=Add Generated with Crush line to commit messages and issues and PRs,default=true"`
}

// JSONSchemaExtend marks the co_authored_by field as deprecated in the schema.
func (Attribution) JSONSchemaExtend(schema *jsonschema.Schema) {
	if schema.Properties != nil {
		if prop, ok := schema.Properties.Get("co_authored_by"); ok {
			prop.Deprecated = true
		}
	}
}

type Options struct {
	ContextPaths         []string    `json:"context_paths,omitempty" jsonschema:"description=Paths to files containing context information for the AI,example=.cursorrules,example=CRUSH.md"`
	SkillsPaths          []string    `json:"skills_paths,omitempty" jsonschema:"description=Paths to directories containing Agent Skills (folders with SKILL.md files),example=~/.config/crush/skills,example=./skills"`
	TUI                  *TUIOptions `json:"tui,omitempty" jsonschema:"description=Terminal user interface options"`
	Debug                bool        `json:"debug,omitempty" jsonschema:"description=Enable debug logging,default=false"`
	DebugLSP             bool        `json:"debug_lsp,omitempty" jsonschema:"description=Enable debug logging for LSP servers,default=false"`
	DisableAutoSummarize bool        `json:"disable_auto_summarize,omitempty" jsonschema:"description=Disable automatic conversation summarization,default=false"`
	// DataDirectory is where Crush keeps per-project state such as
	// the SQLite database and workspace overrides. Relative paths are
	// resolved against the working directory; absolute paths are used
	// verbatim. After defaulting the stored value is always absolute.
	DataDirectory             string       `json:"data_directory,omitempty" jsonschema:"description=Directory for storing application data. Relative paths are resolved against the working directory; absolute paths are used as-is.,default=.crush,example=.crush"`
	DisabledTools             []string     `json:"disabled_tools,omitempty" jsonschema:"description=List of built-in tools to disable and hide from the agent,example=bash,example=sourcegraph"`
	TaskTools                 []string     `json:"task_tools,omitempty" jsonschema:"description=Tools the spawned Task (sub-)agent may call. Built-in tool names plus the special token 'mcp' (grants all MCP tools). Defaults to the read-only set (glob/grep/ls/sourcegraph/view) with no MCP when unset. Disabled tools are still excluded.,example=view,example=write,example=mcp"`
	DisableProviderAutoUpdate bool         `json:"disable_provider_auto_update,omitempty" jsonschema:"description=Disable providers auto-update,default=false"`
	DisableDefaultProviders   bool         `json:"disable_default_providers,omitempty" jsonschema:"description=Ignore all default/embedded providers. When enabled\\, providers must be fully specified in the config file with base_url\\, models\\, and api_key - no merging with defaults occurs,default=false"`
	Attribution               *Attribution `json:"attribution,omitempty" jsonschema:"description=Attribution settings for generated content"`
	DisableMetrics            bool         `json:"disable_metrics,omitempty" jsonschema:"description=Disable sending metrics,default=false"`
	DisableUpdateCheck        bool         `json:"disable_update_check,omitempty" jsonschema:"description=Disable the startup check for new Crush versions,default=false"`
	InitializeAs              string       `json:"initialize_as,omitempty" jsonschema:"description=Name of the context file to create/update during project initialization,default=AGENTS.md,example=AGENTS.md,example=CRUSH.md,example=CLAUDE.md,example=docs/LLMs.md"`
	AutoLSP                   *bool        `json:"auto_lsp,omitempty" jsonschema:"description=Automatically setup LSPs based on root markers,default=true"`
	Progress                  *bool        `json:"progress,omitempty" jsonschema:"description=Show indeterminate progress updates during long operations,default=true"`
	DisableNotifications      bool         `json:"disable_notifications,omitempty" jsonschema:"description=Deprecated: Use notification_style instead. Disable desktop notifications,default=false"`
	NotificationStyle         string       `json:"notification_style,omitempty" jsonschema:"description=Notification style to use. Options: auto (default), native, osc, bell, disabled. Auto selects based on environment: native for local sessions, osc for SSH (with automatic OSC 99/777 detection).,enum=auto,enum=native,enum=osc,enum=bell,enum=disabled,default=auto"`
	DisabledSkills            []string     `json:"disabled_skills,omitempty" jsonschema:"description=List of skill names to disable and hide from the agent,example=crush-config"`
	// Memory controls automatic storage of large tool results so the model
	// can query them later without re-reading the full output.
	EnableMemory         *bool    `json:"enable_memory,omitempty" jsonschema:"description=Store large tool results in memory for later retrieval via memory_list and memory_scroll. Enabled by default.,default=true"`
	MemoryHardLimitBytes int      `json:"memory_hard_limit_bytes,omitempty" jsonschema:"description=Byte length above which tool results are stored in memory (default 8192)"`
	MemoryOverspill      *float64 `json:"memory_overspill,omitempty" jsonschema:"description=Fraction of the hard limit tolerated inline before storing — e.g. 0.20 means up to hard_limit*1.20 is kept inline (default 0.20)"`
	MemoryPreviewLines   int      `json:"memory_preview_lines,omitempty" jsonschema:"description=Lines shown in the inline reference summary returned to the model (default 10)"`
	// MemoryRefuseTools lists tool names that use the refuse strategy: instead
	// of storing oversized results, the model receives an error telling it to
	// re-call the tool with narrower parameters (e.g. view with offset/limit).
	MemoryRefuseTools []string `json:"memory_refuse_tools,omitempty" jsonschema:"description=Tool names that reject oversized results instead of storing them. The model is told to re-call with narrower parameters (e.g. view with offset/limit)."`
	// EnableTaskSelfAssessment injects a follow-up prompt after every run that
	// ends with incomplete todos, asking the model to verify and close out any
	// tasks that were actually finished and to complete any that are genuinely
	// unfinished. Disabled by default.
	EnableTaskSelfAssessment *bool `json:"enable_task_self_assessment,omitempty" jsonschema:"description=After a run that ends with incomplete todos\\, inject a follow-up prompt asking the model to verify and close out finished tasks and complete any that are genuinely unfinished.,default=false"`
	// TaskSelfAssessment tunes the follow-up reminders sent when
	// enable_task_self_assessment is on. When unset, a single reminder is sent
	// (the historical behavior). Set max_reminders higher to keep re-prompting
	// until every task is closed, and target_completion to stop early once a
	// fraction of todos are done.
	TaskSelfAssessment *TaskSelfAssessmentConfig `json:"task_self_assessment,omitempty" jsonschema:"description=Tuning for the task self-assessment reminders (requires enable_task_self_assessment). Controls how many reminders to send and when to stop."`
	// SubagentEnableTaskSelfAssessment overrides EnableTaskSelfAssessment for
	// the spawned Task sub-agent only. When nil, the sub-agent inherits the
	// global EnableTaskSelfAssessment value. This lets you run the follow-up
	// reminders for delegated sub-agent runs independently of the top-level
	// coder (e.g. on for the sub-agent, off for the coder, or vice versa).
	SubagentEnableTaskSelfAssessment *bool `json:"subagent_enable_task_self_assessment,omitempty" jsonschema:"description=Override enable_task_self_assessment for the spawned Task sub-agent only. When unset\\, the sub-agent inherits the global value.,default=false"`
	// SubagentTaskSelfAssessment overrides TaskSelfAssessment tuning for the
	// spawned Task sub-agent only. When nil, the sub-agent inherits the global
	// TaskSelfAssessment tuning.
	SubagentTaskSelfAssessment *TaskSelfAssessmentConfig `json:"subagent_task_self_assessment,omitempty" jsonschema:"description=Override the task self-assessment reminder tuning for the spawned Task sub-agent only. When unset\\, the sub-agent inherits the global tuning."`
	// EnableMidRunSelfAssessment turns on the mid-run nudge that detects a
	// tool-call spiral (e.g. the same search repeated many times) *during* a
	// run and injects a one-shot message — carrying the run's tool-usage stats —
	// asking the model to scope its approach tighter or abandon it. Applies to
	// the top-level coder and the spawned Task sub-agent (which share the run
	// loop). Disabled by default.
	EnableMidRunSelfAssessment *bool `json:"enable_midrun_self_assessment,omitempty" jsonschema:"description=During a run\\, detect a tool-call spiral (e.g. the same search repeated many times) and inject a one-shot nudge with the run's tool-usage stats asking the model to scope tighter or abandon the approach.,default=false"`
	// MidRunSelfAssessment tunes the mid-run nudge (window, repeat threshold,
	// max injections). When unset, defaults are used.
	MidRunSelfAssessment *MidRunSelfAssessmentConfig `json:"midrun_self_assessment,omitempty" jsonschema:"description=Tuning for the mid-run self-assessment nudge (requires enable_midrun_self_assessment). Controls the detection window\\, repeat threshold\\, and how many nudges may be injected per run."`
	// SubagentEnableMidRunSelfAssessment overrides EnableMidRunSelfAssessment
	// for the spawned Task sub-agent only. When nil, the sub-agent inherits the
	// global value.
	SubagentEnableMidRunSelfAssessment *bool `json:"subagent_enable_midrun_self_assessment,omitempty" jsonschema:"description=Override enable_midrun_self_assessment for the spawned Task sub-agent only. When unset\\, the sub-agent inherits the global value.,default=false"`
	// SubagentMidRunSelfAssessment overrides MidRunSelfAssessment tuning for the
	// spawned Task sub-agent only. It merges field-by-field over the global
	// tuning: set just the fields you want to change for the sub-agent and the
	// rest are inherited from the global values (then built-in defaults).
	SubagentMidRunSelfAssessment *MidRunSelfAssessmentConfig `json:"subagent_midrun_self_assessment,omitempty" jsonschema:"description=Override the mid-run self-assessment tuning for the spawned Task sub-agent only. Merges field-by-field over the global tuning: set only the fields you want to change\\, the rest are inherited."`
	// EnableContextBudget turns on the context-budget nudge: during a run, when
	// the prompt approaches the model's context window, inject a one-shot message
	// telling the model to start wrapping up (and, past the hard threshold, to
	// stop and write its deliverable now). Applies to the top-level coder and the
	// spawned Task sub-agent. Inert when the model's context window is unknown.
	// Disabled by default.
	EnableContextBudget *bool `json:"enable_context_budget,omitempty" jsonschema:"description=During a run\\, watch how full the model's context window is and inject a one-shot nudge to wrap up (soft) or stop and write the deliverable now (hard) as usage crosses thresholds. Inert when the context window is unknown.,default=false"`
	// ContextBudget tunes the context-budget nudge (warn/hard fraction of the
	// context window). When unset, defaults are used.
	ContextBudget *ContextBudgetConfig `json:"context_budget,omitempty" jsonschema:"description=Tuning for the context-budget nudge (requires enable_context_budget). Controls the warn and hard fractions of the context window at which the nudges fire."`
	// SubagentEnableContextBudget overrides EnableContextBudget for the spawned
	// Task sub-agent only. When nil, the sub-agent inherits the global value.
	SubagentEnableContextBudget *bool `json:"subagent_enable_context_budget,omitempty" jsonschema:"description=Override enable_context_budget for the spawned Task sub-agent only. When unset\\, the sub-agent inherits the global value.,default=false"`
	// SubagentContextBudget overrides ContextBudget tuning for the spawned Task
	// sub-agent only. It merges field-by-field over the global tuning.
	SubagentContextBudget *ContextBudgetConfig `json:"subagent_context_budget,omitempty" jsonschema:"description=Override the context-budget tuning for the spawned Task sub-agent only. Merges field-by-field over the global tuning: set only the fields you want to change\\, the rest are inherited."`
	// EnableTimeBudget turns on the time-budget nudge: during a run, when
	// wall-clock elapsed crosses a fraction of the configured budget, inject a
	// one-shot message telling the model to pace itself (and, once the budget is
	// reached, to stop and write its deliverable now). Requires a budget_minutes
	// to be set. Applies to the coder and the spawned Task sub-agent. Disabled by
	// default.
	EnableTimeBudget *bool `json:"enable_time_budget,omitempty" jsonschema:"description=During a run\\, watch wall-clock elapsed against a budget and inject a one-shot nudge to pace yourself (soft) or stop and write the deliverable now (hard) as time runs out. Requires time_budget.budget_minutes to be set.,default=false"`
	// TimeBudget tunes the time-budget nudge (budget_minutes + warn fraction).
	// The nudge stays inert until budget_minutes is set above 0.
	TimeBudget *TimeBudgetConfig `json:"time_budget,omitempty" jsonschema:"description=Tuning for the time-budget nudge (requires enable_time_budget). Set budget_minutes to the wall-clock budget for a run; warn_percent controls when the soft nudge fires."`
	// SubagentEnableTimeBudget overrides EnableTimeBudget for the spawned Task
	// sub-agent only. When nil, the sub-agent inherits the global value.
	SubagentEnableTimeBudget *bool `json:"subagent_enable_time_budget,omitempty" jsonschema:"description=Override enable_time_budget for the spawned Task sub-agent only. When unset\\, the sub-agent inherits the global value.,default=false"`
	// SubagentTimeBudget overrides TimeBudget tuning for the spawned Task
	// sub-agent only. It merges field-by-field over the global tuning — useful to
	// give a sub-agent investigation a tighter budget than the coder.
	SubagentTimeBudget *TimeBudgetConfig `json:"subagent_time_budget,omitempty" jsonschema:"description=Override the time-budget tuning for the spawned Task sub-agent only. Merges field-by-field over the global tuning: set only the fields you want to change\\, the rest are inherited."`
	// CompactTools replaces verbose tool descriptions with shorter versions to
	// reduce prompt token usage. Useful for local models with smaller context
	// windows. Affects memory_scroll, memory_list, memory_grep, file_write,
	// file_edit, and file_grep.
	CompactTools bool `json:"compact_tools,omitempty" jsonschema:"description=Use shorter tool descriptions to reduce prompt token usage. Useful for local/small-context models.,default=false"`
	// CompactPrompt replaces the full coder system prompt with a shorter variant
	// that preserves all behavioral rules but strips verbose examples, duplicate
	// explanations, and redundant sections. Saves ~4,500–5,000 tokens.
	// Recommended for local models with context windows under 64K.
	CompactPrompt bool `json:"compact_prompt,omitempty" jsonschema:"description=Use a shorter system prompt to reduce token usage. Recommended for local/small-context models.,default=false"`
	// PromptPaths fully overrides the coder system prompt: when set, the listed
	// files are concatenated (in order) and used as the coder prompt template
	// instead of the built-in one. The concatenated content is still run through
	// the same Go text/template engine, so it may use {{.WorkingDir}},
	// {{.GitStatus}}, {{range .ContextFiles}}, etc. (plain text passes through
	// unchanged). When this override is active the auto-discovered context files
	// (CLAUDE.md, CRUSH.md, AGENTS.md, …) are NOT appended — the injected files
	// are the whole prompt. Relative paths resolve against the working directory;
	// ~ and $VARs are expanded. A missing/unreadable file is a hard error.
	// Takes precedence over compact_prompt. Also inherited by the spawned Task
	// sub-agent when subagent_prompt_paths is unset (see SubagentPromptPaths).
	// Useful for running crush as an execution engine with a fully custom prompt.
	PromptPaths []string `json:"prompt_paths,omitempty" jsonschema:"description=Files concatenated (in order) to fully override the coder system prompt. Rendered as a Go text/template (can use {{.WorkingDir}}\\, {{.GitStatus}}\\, {{range .ContextFiles}}). Suppresses auto-discovered context files. Overrides compact_prompt. The sub-agent inherits this unless subagent_prompt_paths is set.,example=prompts/base.md,example=prompts/rules.md"`
	// SubagentPromptPaths is the sub-agent counterpart of PromptPaths: when set,
	// the listed files are concatenated (in order) and used as the spawned Task
	// sub-agent's prompt template instead of the built-in task prompt. Same
	// semantics as PromptPaths (rendered as a Go text/template, auto-discovered
	// context files suppressed, relative paths resolve against the working dir,
	// missing file = hard error). When this is unset the sub-agent inherits
	// PromptPaths; set it only to give the sub-agent a prompt that differs from
	// the coder's. The effective resolution is: subagent_prompt_paths → prompt_paths
	// → built-in task template.
	SubagentPromptPaths []string `json:"subagent_prompt_paths,omitempty" jsonschema:"description=Files concatenated (in order) to fully override the spawned Task sub-agent's system prompt. Same semantics as prompt_paths but applies only to the sub-agent. When unset the sub-agent inherits prompt_paths; set this only to diverge from the coder.,example=prompts/subagent.md"`
	// SubagentRoles maps a role name to the prompt file(s) that define a
	// specialized Task sub-agent. When non-empty, the coder is granted the
	// list_roles tool (to discover the catalog) and may pass a `role` argument
	// to the agent tool naming one of these keys; the spawned sub-agent's system
	// prompt is then built from that role's files. Each value has the same
	// semantics as PromptPaths (concatenated in order, rendered as a Go
	// text/template, auto-discovered context files suppressed, relative paths
	// resolve against the working dir, missing file = hard error). When the model
	// passes no role the sub-agent uses the normal resolution
	// (subagent_prompt_paths → prompt_paths → built-in task template).
	SubagentRoles map[string][]string `json:"subagent_roles,omitempty" jsonschema:"description=Maps a role name to the prompt file(s) defining a specialized Task sub-agent. When set\\, the coder gets the list_roles tool and may pass a 'role' to the agent tool to spawn that sub-agent. Each value has the same semantics as prompt_paths (concatenated\\, Go-templated\\, context files suppressed)."`
	// SummarizePrompt runs an async bootstrap step at session start that uses
	// the small model to further compress the system prompt. The first turn uses
	// the base prompt (full or compact); subsequent turns use the summarized
	// version. A log line is emitted when the summarization completes. Requires
	// the small model to be configured.
	SummarizePrompt bool `json:"summarize_prompt,omitempty" jsonschema:"description=Async: use the small model to compress the system prompt at session start. First turn uses the base prompt; later turns use the summarized version.,default=false"`
	// StreamSubagentOutput controls whether the live output of spawned
	// sub-agents (the "agent" tool) is streamed to stdout in non-interactive
	// mode (crush run). Sub-agents run in child sessions whose output is
	// normally hidden and surfaced only as the tool result handed back to the
	// top-level agent. When enabled, their assistant text is streamed to stdout
	// alongside the top-level agent's output. Has no effect in the interactive
	// TUI, which renders child sessions on its own.
	StreamSubagentOutput bool `json:"stream_subagent_output,omitempty" jsonschema:"description=In non-interactive mode (crush run)\\, stream the live output of spawned sub-agents (the agent tool) to stdout in addition to the top-level agent's output. Sub-agents run in child sessions whose output is otherwise surfaced only as a tool result.,default=false"`
}

// TaskSelfAssessmentConfig tunes the follow-up reminders Crush injects when
// enable_task_self_assessment is on and a run ends with incomplete todos.
//
// Reminders stop as soon as the completion target is reached or the cap is
// hit, whichever comes first, so MaxReminders always bounds the worst case.
type TaskSelfAssessmentConfig struct {
	// MaxReminders is the maximum number of follow-up reminders to send while
	// todos remain incomplete. Values < 1 fall back to the default of 1.
	// Raise it to keep re-prompting the model until every task is closed.
	MaxReminders int `json:"max_reminders,omitempty" jsonschema:"description=Maximum number of follow-up reminders to send while todos remain incomplete. Raise it to keep re-prompting until all tasks are closed.,default=1"`
	// TargetCompletion stops reminders once this fraction (0-1] of todos are
	// completed. Values <= 0 or > 1 fall back to the default of 1.0 (every
	// task must be closed before reminders stop).
	TargetCompletion float64 `json:"target_completion,omitempty" jsonschema:"description=Stop sending reminders once this fraction (0-1) of todos are completed. Defaults to 1.0 (all tasks).,default=1"`
}

// MidRunSelfAssessmentConfig tunes the mid-run self-assessment nudge. Unlike
// task self-assessment (which re-prompts after a run ends), this fires *during*
// a run when the agent appears to be spiraling on tool calls — e.g. running the
// same search over and over. When tripped, a one-shot message carrying the
// run's tool-usage stats is injected into the next step, asking the model to
// either scope its approach tighter or abandon it, then the run continues.
type MidRunSelfAssessmentConfig struct {
	// Window is the number of most recent steps inspected for repetition. A
	// value < 1 falls back to the default of 7.
	Window int `json:"window,omitempty" jsonschema:"description=Number of most recent steps inspected when looking for a tool-call spiral. Defaults to 7.,default=7"`
	// RepeatThreshold trips the nudge once any single tool has been called at
	// least this many times within the window. A value < 2 falls back to the
	// default of 5.
	RepeatThreshold int `json:"repeat_threshold,omitempty" jsonschema:"description=Trip the nudge once any single tool is called at least this many times within the window. Defaults to 5.,default=5"`
	// MaxInjections caps how many nudges are injected per run. A value < 1 falls
	// back to the default of 5. After each injection a cooldown of one full
	// window must pass before the nudge can trip again.
	MaxInjections int `json:"max_injections,omitempty" jsonschema:"description=Maximum number of mid-run nudges injected per run. Defaults to 5.,default=5"`
}

// ContextBudgetConfig tunes the context-budget nudge. While
// enable_context_budget is on, Crush watches how full the model's context
// window is and injects a one-shot message when usage crosses a threshold: a
// soft "start wrapping up" at warn_percent and a harder "stop and write your
// deliverable now" at hard_percent. Each threshold fires at most once per run.
// Inert when the model's context window is unknown (it can't compute a percent).
type ContextBudgetConfig struct {
	// WarnPercent is the fraction (0-1] of the context window at which the soft
	// "start consolidating" nudge fires. A value <= 0 or > 1 falls back to 0.70.
	WarnPercent float64 `json:"warn_percent,omitempty" jsonschema:"description=Fraction (0-1) of the context window at which to inject a soft 'start wrapping up' nudge. Defaults to 0.70.,default=0.7"`
	// HardPercent is the fraction (0-1] of the context window at which the hard
	// "stop now and write your deliverable" nudge fires. A value <= 0 or > 1
	// falls back to 0.85.
	HardPercent float64 `json:"hard_percent,omitempty" jsonschema:"description=Fraction (0-1) of the context window at which to inject a hard 'stop now and write your deliverable' nudge. Defaults to 0.85.,default=0.85"`
}

// TimeBudgetConfig tunes the time-budget nudge. While enable_time_budget is on
// and budget_minutes is set, Crush watches wall-clock elapsed for the run and
// injects a one-shot message when it crosses a threshold: a soft "pace
// yourself" at warn_percent and a hard "time is up, write your deliverable now"
// once the budget is reached. Each threshold fires at most once per run. The
// budget is per run (per user turn for the coder, per spawn for a sub-agent).
type TimeBudgetConfig struct {
	// BudgetMinutes is the wall-clock budget for a run, in minutes. There is no
	// default: a value <= 0 leaves the nudge inert even when enabled.
	BudgetMinutes float64 `json:"budget_minutes,omitempty" jsonschema:"description=Wall-clock budget for a single run in minutes. No default — the nudge stays inert until this is set above 0."`
	// WarnPercent is the fraction (0-1] of the budget at which the soft "pace
	// yourself" nudge fires. A value <= 0 or > 1 falls back to 0.75. The hard
	// wrap-up nudge always fires once elapsed reaches the full budget.
	WarnPercent float64 `json:"warn_percent,omitempty" jsonschema:"description=Fraction (0-1) of the time budget at which to inject a soft 'pace yourself' nudge. Defaults to 0.75. The hard wrap-up nudge fires at 100%% of the budget.,default=0.75"`
}

type MCPs map[string]MCPConfig

type MCP struct {
	Name string    `json:"name"`
	MCP  MCPConfig `json:"mcp"`
}

func (m MCPs) Sorted() []MCP {
	sorted := make([]MCP, 0, len(m))
	for k, v := range m {
		sorted = append(sorted, MCP{
			Name: k,
			MCP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b MCP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

type LSPs map[string]LSPConfig

type LSP struct {
	Name string    `json:"name"`
	LSP  LSPConfig `json:"lsp"`
}

func (l LSPs) Sorted() []LSP {
	sorted := make([]LSP, 0, len(l))
	for k, v := range l {
		sorted = append(sorted, LSP{
			Name: k,
			LSP:  v,
		})
	}
	slices.SortFunc(sorted, func(a, b LSP) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

// ResolvedEnv returns m.Env with every value expanded through the
// given resolver. The returned slice is of the form "KEY=value" sorted
// by key so callers get deterministic output; the receiver's Env map is
// not mutated. On the first resolution failure it returns nil and an
// error that identifies the offending key; the inner resolver error is
// already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work. Callers are expected to surface it
// (for MCP, via StateError on the status card) rather than silently
// spawn the server with an empty credential.
//
// The resolver choice matters: in server mode pass the shell resolver
// so $VAR / $(cmd) expand; in client mode pass IdentityResolver so the
// template is forwarded verbatim and expansion happens on the server.
func (m MCPConfig) ResolvedEnv(r VariableResolver) ([]string, error) {
	return resolveEnvs(m.Env, r)
}

// ResolvedArgs returns m.Args with every element expanded through the
// given resolver. A fresh slice is allocated; m.Args is never mutated.
// On the first resolution failure it returns nil and an error
// identifying the offending positional index; the inner resolver error
// is already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work.
//
// See ResolvedEnv for guidance on picking a resolver.
func (m MCPConfig) ResolvedArgs(r VariableResolver) ([]string, error) {
	if len(m.Args) == 0 {
		return nil, nil
	}
	out := make([]string, len(m.Args))
	for i, a := range m.Args {
		v, err := r.ResolveValue(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ResolvedURL returns m.URL expanded through the given resolver. The
// receiver is not mutated. Errors from the resolver are already
// sanitized by ResolveValue and are wrapped with %w for errors.Is/As.
//
// URLs run through the same shell-expansion pipeline as the other
// fields, so a literal '$' (e.g. OData query strings containing
// $filter/$select) must be escaped as '\$' or '${DOLLAR:-$}' to avoid
// being interpreted as a variable reference. Same constraint already
// applies to command, args, env, and headers.
//
// See ResolvedEnv for guidance on picking a resolver.
func (m MCPConfig) ResolvedURL(r VariableResolver) (string, error) {
	if m.URL == "" {
		return "", nil
	}
	v, err := r.ResolveValue(m.URL)
	if err != nil {
		return "", fmt.Errorf("url: %w", err)
	}
	return v, nil
}

// ResolvedHeaders returns m.Headers with every value expanded through
// the given resolver. A fresh map is allocated; m.Headers is never
// mutated. On the first resolution failure it returns nil and an error
// identifying the offending header name; the inner resolver error is
// already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work.
//
// A header whose value resolves to the empty string (unset bare $VAR
// under lenient nounset, $(echo), or literal "") is omitted from the
// returned map — sending "X-Auth:" with an empty value is rejected by
// some providers and the user's intent in "optional, env-gated
// header" is clearly "absent when the var isn't set."
//
// See ResolvedEnv for guidance on picking a resolver.
func (m MCPConfig) ResolvedHeaders(r VariableResolver) (map[string]string, error) {
	if len(m.Headers) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(m.Headers))
	// Sort keys so failures are reported deterministically when more
	// than one header would fail.
	keys := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, err := r.ResolveValue(m.Headers[k])
		if err != nil {
			return nil, fmt.Errorf("header %s: %w", k, err)
		}
		if v == "" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// ResolvedArgs returns l.Args with every element expanded through the
// given resolver. A fresh slice is allocated; l.Args is never mutated.
// On the first resolution failure it returns nil and an error
// identifying the offending positional index; the inner resolver error
// is already sanitized by ResolveValue and is wrapped with %w so
// errors.Is/As continues to work.
//
// Empty resolved values are kept (a deliberate "empty positional arg"
// like --flag "" is sometimes valid), matching MCPConfig.ResolvedArgs.
//
// The resolver choice matters: in server mode pass the shell resolver
// so $VAR / $(cmd) expand; in client mode pass IdentityResolver so the
// template is forwarded verbatim.
func (l LSPConfig) ResolvedArgs(r VariableResolver) ([]string, error) {
	if len(l.Args) == 0 {
		return nil, nil
	}
	out := make([]string, len(l.Args))
	for i, a := range l.Args {
		v, err := r.ResolveValue(a)
		if err != nil {
			return nil, fmt.Errorf("arg %d: %w", i, err)
		}
		out[i] = v
	}
	return out, nil
}

// ResolvedEnv returns l.Env with every value expanded through the
// given resolver. A fresh map is allocated; l.Env is never mutated.
// On the first resolution failure it returns nil and an error that
// identifies the offending key; the inner resolver error is already
// sanitized by ResolveValue and is wrapped with %w so errors.Is/As
// continues to work.
//
// Empty resolved values are kept ("FOO=" is a legitimate request;
// opt out via ${VAR:+...}), matching MCPConfig.ResolvedEnv.
//
// Shape note: this returns map[string]string rather than the []string
// shape MCPConfig.ResolvedEnv uses because the consumer
// (powernap.ClientConfig.Environment in internal/lsp/client.go) takes
// a map directly — returning a []string here would only force a
// round-trip back to a map at the call site.
//
// See ResolvedArgs for guidance on picking a resolver.
func (l LSPConfig) ResolvedEnv(r VariableResolver) (map[string]string, error) {
	if len(l.Env) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(l.Env))
	// Sort keys so failures are reported deterministically when more
	// than one value would fail.
	keys := make([]string, 0, len(l.Env))
	for k := range l.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, err := r.ResolveValue(l.Env[k])
		if err != nil {
			return nil, fmt.Errorf("env %q: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

type Agent struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// This is the id of the system prompt used by the agent
	Disabled bool `json:"disabled,omitempty"`

	Model SelectedModelType `json:"model" jsonschema:"required,description=The model type to use for this agent,enum=large,enum=small,default=large"`

	// The available tools for the agent
	//  if this is nil, all tools are available
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// this tells us which MCPs are available for this agent
	//  if this is empty all mcps are available
	//  the string array is the list of tools from the AllowedMCP the agent has available
	//  if the string array is nil, all tools from the AllowedMCP are available
	AllowedMCP map[string][]string `json:"allowed_mcp,omitempty"`

	// Overrides the context paths for this agent
	ContextPaths []string `json:"context_paths,omitempty"`

	// EnableTaskSelfAssessment overrides Options.EnableTaskSelfAssessment for
	// this agent. When nil, the global Options value applies. Set it to enable
	// (or disable) the incomplete-todo follow-up reminders for this agent
	// independently of the global default — e.g. on for the spawned Task
	// sub-agent but off for the top-level coder, or vice versa.
	EnableTaskSelfAssessment *bool `json:"enable_task_self_assessment,omitempty" jsonschema:"description=Per-agent override for enable_task_self_assessment. When unset\\, the global options value applies."`

	// TaskSelfAssessment overrides Options.TaskSelfAssessment for this agent.
	// When nil, the global Options tuning applies. Requires self-assessment to
	// be enabled (either here or globally).
	TaskSelfAssessment *TaskSelfAssessmentConfig `json:"task_self_assessment,omitempty" jsonschema:"description=Per-agent override for the task self-assessment reminder tuning. When unset\\, the global options value applies."`
}

type Tools struct {
	Ls   ToolLs   `json:"ls,omitzero"`
	Grep ToolGrep `json:"grep,omitzero"`
}

type ToolLs struct {
	MaxDepth *int `json:"max_depth,omitempty" jsonschema:"description=Maximum depth for the ls tool,default=0,example=10"`
	MaxItems *int `json:"max_items,omitempty" jsonschema:"description=Maximum number of items to return for the ls tool,default=1000,example=100"`
}

// Limits returns the user-defined max-depth and max-items, or their defaults.
func (t ToolLs) Limits() (depth, items int) {
	return ptrValOr(t.MaxDepth, 0), ptrValOr(t.MaxItems, 0)
}

type ToolGrep struct {
	Timeout *time.Duration `json:"timeout,omitempty" jsonschema:"description=Timeout for the grep tool call,default=5s,example=10s"`
}

// GetTimeout returns the user-defined timeout or the default.
func (t ToolGrep) GetTimeout() time.Duration {
	return ptrValOr(t.Timeout, 5*time.Second)
}

// HookConfig defines a user-configured shell command that fires on a hook
// event (e.g. PreToolUse). This is a pure-data struct: matcher compilation
// is owned by hooks.Runner so a JSON round-trip, merge, or reload can't
// silently drop compiled state.
type HookConfig struct {
	// Regex pattern tested against the tool name. Empty means match all.
	Matcher string `json:"matcher,omitempty" jsonschema:"description=Regex pattern tested against the tool name. Empty means match all tools."`
	// Shell command to execute.
	Command string `json:"command" jsonschema:"required,description=Shell command to execute when the hook fires"`
	// Timeout in seconds. Default 30.
	Timeout int `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for the hook command,default=30"`
}

// TimeoutDuration returns the hook timeout as a time.Duration, defaulting
// to 30s.
func (h *HookConfig) TimeoutDuration() time.Duration {
	if h.Timeout <= 0 {
		return 30 * time.Second
	}
	return time.Duration(h.Timeout) * time.Second
}

// Config holds the configuration for crush.
type Config struct {
	Schema string `json:"$schema,omitempty"`

	// We currently only support large/small as values here.
	Models map[SelectedModelType]SelectedModel `json:"models,omitempty" jsonschema:"description=Model configurations for different model types,example={\"large\":{\"model\":\"gpt-4o\",\"provider\":\"openai\"}}"`

	// Recently used models stored in the data directory config.
	RecentModels map[SelectedModelType][]SelectedModel `json:"recent_models,omitempty" jsonschema:"-"`

	// The providers that are configured
	Providers *csync.Map[string, ProviderConfig] `json:"providers,omitempty" jsonschema:"description=AI provider configurations"`

	MCP MCPs `json:"mcp,omitempty" jsonschema:"description=Model Context Protocol server configurations"`

	LSP LSPs `json:"lsp,omitempty" jsonschema:"description=Language Server Protocol configurations"`

	Options *Options `json:"options,omitempty" jsonschema:"description=General application options"`

	Permissions *Permissions `json:"permissions,omitempty" jsonschema:"description=Permission settings for tool usage"`

	Tools Tools `json:"tools,omitzero" jsonschema:"description=Tool configurations"`

	Hooks map[string][]HookConfig `json:"hooks,omitempty" jsonschema:"description=User-defined shell commands that fire on hook events (e.g. PreToolUse)"`

	Agents map[string]Agent `json:"-"`
}

func (c *Config) EnabledProviders() []ProviderConfig {
	var enabled []ProviderConfig
	for p := range c.Providers.Seq() {
		if !p.Disable {
			enabled = append(enabled, p)
		}
	}
	return enabled
}

// IsConfigured  return true if at least one provider is configured
func (c *Config) IsConfigured() bool {
	return len(c.EnabledProviders()) > 0
}

func (c *Config) GetModel(provider, model string) *catwalk.Model {
	if providerConfig, ok := c.Providers.Get(provider); ok {
		for _, m := range providerConfig.Models {
			if m.ID == model {
				return &m
			}
		}
	}
	return nil
}

func (c *Config) GetProviderForModel(modelType SelectedModelType) *ProviderConfig {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	if providerConfig, ok := c.Providers.Get(model.Provider); ok {
		return &providerConfig
	}
	return nil
}

func (c *Config) GetModelByType(modelType SelectedModelType) *catwalk.Model {
	model, ok := c.Models[modelType]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) LargeModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeLarge]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

func (c *Config) SmallModel() *catwalk.Model {
	model, ok := c.Models[SelectedModelTypeSmall]
	if !ok {
		return nil
	}
	return c.GetModel(model.Provider, model.Model)
}

const maxRecentModelsPerType = 5

func allToolNames() []string {
	return []string{
		"agent",
		"bash",
		"crush_info",
		"crush_logs",
		"job_output",
		"job_kill",
		"download",
		"edit",
		"multiedit",
		"lsp_diagnostics",
		"lsp_references",
		"lsp_restart",
		"fetch",
		"agentic_fetch",
		"glob",
		"grep",
		"ls",
		"sourcegraph",
		"todos",
		"view",
		"write",
		"list_mcp_resources",
		"read_mcp_resource",
		"memory_list",
		"memory_scroll",
	}
}

func resolveAllowedTools(allTools []string, disabledTools []string) []string {
	if disabledTools == nil {
		return allTools
	}
	// filter out disabled tools (exclude mode)
	return filterSlice(allTools, disabledTools, false)
}

func resolveReadOnlyTools(tools []string) []string {
	readOnlyTools := []string{"glob", "grep", "ls", "sourcegraph", "view"}
	// filter to only include tools that are in allowedtools (include mode)
	return filterSlice(tools, readOnlyTools, true)
}

// resolveTaskTools resolves the built-in tools available to the Task (sub-)agent.
// When task_tools is unset it defaults to the read-only set; when configured it is
// the configured list intersected with allowedTools (so disabled_tools still wins).
// The special token "mcp" is not a built-in tool — it is handled by resolveTaskMCP
// and ignored here (it simply won't match any built-in name).
func resolveTaskTools(allowedTools []string, configured []string) []string {
	if len(configured) == 0 {
		return resolveReadOnlyTools(allowedTools)
	}
	return filterSlice(allowedTools, configured, true)
}

// taskMCPToken in task_tools grants the Task (sub-)agent access to ALL MCP tools.
const taskMCPToken = "mcp"

// resolveTaskMCP decides the Task agent's MCP access from task_tools: if the special
// "mcp" token is present, return nil (no restriction = all MCP tools); otherwise an
// empty map (no MCP tools) — preserving the historical default.
func resolveTaskMCP(configured []string) map[string][]string {
	if slices.Contains(configured, taskMCPToken) {
		return nil
	}
	return map[string][]string{}
}

func filterSlice(data []string, mask []string, include bool) []string {
	var filtered []string
	for _, s := range data {
		// if include is true, we include items that ARE in the mask
		// if include is false, we include items that are NOT in the mask
		if include == slices.Contains(mask, s) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (c *Config) SetupAgents() {
	allowedTools := resolveAllowedTools(allToolNames(), c.Options.DisabledTools)

	// The list_roles tool is only useful when subagent roles are configured, so
	// grant it to the coder only then (and never if it was explicitly disabled).
	// Keeping it off by default avoids adding a dead tool to the prompt schema.
	coderTools := allowedTools
	if len(c.Options.SubagentRoles) > 0 && !slices.Contains(c.Options.DisabledTools, ListRolesToolName) {
		coderTools = append(slices.Clone(coderTools), ListRolesToolName)
	}

	agents := map[string]Agent{
		AgentCoder: {
			ID:           AgentCoder,
			Name:         "Coder",
			Description:  "An agent that helps with executing coding tasks.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			AllowedTools: coderTools,
		},

		AgentTask: {
			ID:           AgentTask,
			Name:         "Task",
			Description:  "An agent that helps with searching for context and finding implementation details.",
			Model:        SelectedModelTypeLarge,
			ContextPaths: c.Options.ContextPaths,
			// Built-in tools for the sub-agent come from `task_tools` (defaults to the read-only
			// set when unset). Including the special token "mcp" in task_tools also grants the
			// sub-agent ALL MCP tools, so it can investigate via MCP-backed data sources.
			AllowedTools: resolveTaskTools(allowedTools, c.Options.TaskTools),
			AllowedMCP:   resolveTaskMCP(c.Options.TaskTools),
			// Sub-agent self-assessment overrides. When these are nil the
			// coordinator falls back to the global Options values, so an unset
			// override means "inherit the global default". The coder agent is
			// left unset here for the same reason — it always resolves against
			// the global Options directly.
			EnableTaskSelfAssessment: c.Options.SubagentEnableTaskSelfAssessment,
			TaskSelfAssessment:       c.Options.SubagentTaskSelfAssessment,
		},
	}
	c.Agents = agents
}

func (c *ProviderConfig) TestConnection(resolver VariableResolver) error {
	var (
		providerID = catwalk.InferenceProvider(c.ID)
		testURL    = ""
		headers    = make(map[string]string)
		apiKey, _  = resolver.ResolveValue(c.APIKey)
	)

	switch providerID {
	case catwalk.InferenceProviderMiniMax, catwalk.InferenceProviderMiniMaxChina:
		// NOTE: MiniMax has no good endpoint we can use to validate the API key.
		return nil
	case catwalk.InferenceProviderAlibabaSingapore:
		// NOTE: Alibaba has no good endpoint we can use to validate the API key.
		// Let's at least check the pattern.
		if !strings.HasPrefix(apiKey, "sk-") {
			return fmt.Errorf("invalid API key format for provider %s", c.ID)
		}
		return nil
	}

	switch c.Type {
	case catwalk.TypeOpenAI, catwalk.TypeOpenAICompat, catwalk.TypeOpenRouter:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://api.openai.com/v1")

		switch providerID {
		case catwalk.InferenceProviderOpenRouter:
			testURL = baseURL + "/credits"
		case catwalk.InferenceProviderOpenCodeGo:
			testURL = strings.Replace(baseURL, "/go", "", 1) + "/models"
		default:
			testURL = baseURL + "/models"
		}

		headers["Authorization"] = "Bearer " + apiKey
	case catwalk.TypeAnthropic:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://api.anthropic.com/v1")

		switch providerID {
		case catwalk.InferenceKimiCoding:
			testURL = baseURL + "/v1/models"
		default:
			testURL = baseURL + "/models"
		}

		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = "2023-06-01"
	case catwalk.TypeGoogle:
		baseURL, _ := resolver.ResolveValue(c.BaseURL)
		baseURL = cmp.Or(baseURL, "https://generativelanguage.googleapis.com")
		testURL = baseURL + "/v1beta/models?key=" + url.QueryEscape(apiKey)
	case catwalk.TypeBedrock:
		// NOTE: Bedrock has a `/foundation-models` endpoint that we could in
		// theory use, but apparently the authorization is region-specific,
		// so it's not so trivial.
		if strings.HasPrefix(apiKey, "ABSK") { // Bedrock API keys
			return nil
		}
		return errors.New("not a valid bedrock api key")
	case catwalk.TypeVercel:
		// NOTE: Vercel does not validate API keys on the `/models` endpoint.
		if strings.HasPrefix(apiKey, "vck_") { // Vercel API keys
			return nil
		}
		return errors.New("not a valid vercel api key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range c.ExtraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create request for provider %s: %w", c.ID, err)
	}
	defer resp.Body.Close()

	switch providerID {
	case catwalk.InferenceProviderZAI:
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	default:
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to connect to provider %s: %s", c.ID, resp.Status)
		}
	}
	return nil
}

// resolveEnvs expands every value in envs through the given resolver
// and returns a fresh "KEY=value" slice sorted by key. The input map is
// not mutated. On the first resolution failure it returns nil and an
// error identifying the offending variable; the inner resolver error is
// already sanitized by ResolveValue and is wrapped with %w.
func resolveEnvs(envs map[string]string, r VariableResolver) ([]string, error) {
	if len(envs) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	res := make([]string, 0, len(envs))
	for _, k := range keys {
		v, err := r.ResolveValue(envs[k])
		if err != nil {
			return nil, fmt.Errorf("env %s: %w", k, err)
		}
		res = append(res, fmt.Sprintf("%s=%s", k, v))
	}
	return res, nil
}

func ptrValOr[T any](t *T, el T) T {
	if t == nil {
		return el
	}
	return *t
}
