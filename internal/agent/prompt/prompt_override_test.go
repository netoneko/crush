package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// newTestStore loads a real ConfigStore rooted at a temp working dir. The dir
// doubles as the place we drop prompt/context files for the override tests.
func newTestStore(t *testing.T) (*config.ConfigStore, string) {
	t.Helper()
	wd := t.TempDir()
	store, err := config.Init(wd, t.TempDir(), false)
	require.NoError(t, err)
	return store, wd
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestConcatPromptFiles_ConcatenatesInOrder(t *testing.T) {
	store, wd := newTestStore(t)
	writeFile(t, wd, "a.md", "AAA")
	writeFile(t, wd, "b.md", "BBB")

	got, err := ConcatPromptFiles([]string{"a.md", "b.md"}, store)
	require.NoError(t, err)
	require.Equal(t, "AAA\n\nBBB", got)
}

func TestConcatPromptFiles_MissingFileIsHardError(t *testing.T) {
	store, _ := newTestStore(t)
	_, err := ConcatPromptFiles([]string{"does-not-exist.md"}, store)
	require.Error(t, err)
}

func TestPromptOverride_RendersTemplateAndSuppressesContextFiles(t *testing.T) {
	store, wd := newTestStore(t)

	// A context file that WOULD be auto-injected by the default prompt.
	writeFile(t, wd, "CRUSH.md", "SECRET-PROJECT-CONTEXT")

	// The override template uses a dynamic var to prove it is rendered, and
	// ranges over ContextFiles to prove they are suppressed (range emits nothing).
	writeFile(t, wd, "base.md", "ENGINE PROMPT wd={{.WorkingDir}}{{range .ContextFiles}} CTX:{{.Content}}{{end}}")

	tmpl, err := ConcatPromptFiles([]string{"base.md"}, store)
	require.NoError(t, err)

	p, err := NewPrompt("coder", tmpl, WithoutContextFiles())
	require.NoError(t, err)

	out, err := p.Build(context.Background(), "prov", "model", store)
	require.NoError(t, err)

	require.Contains(t, out, "ENGINE PROMPT wd=")
	require.NotContains(t, out, "SECRET-PROJECT-CONTEXT", "context files must be suppressed under override")
}

func TestPrompt_ContextFilesLoadedByDefault(t *testing.T) {
	store, wd := newTestStore(t)
	writeFile(t, wd, "CRUSH.md", "SECRET-PROJECT-CONTEXT")

	// Without WithoutContextFiles, the same range should emit the context file,
	// confirming the suppression in the test above is meaningful (not vacuous).
	p, err := NewPrompt("coder", "{{range .ContextFiles}}CTX:{{.Content}}{{end}}")
	require.NoError(t, err)

	out, err := p.Build(context.Background(), "prov", "model", store)
	require.NoError(t, err)
	require.True(t, strings.Contains(out, "SECRET-PROJECT-CONTEXT"))
}
