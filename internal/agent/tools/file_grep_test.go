package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func runFileGrep(t *testing.T, workingDir, pattern, path, include string) fantasy.ToolResponse {
	t.Helper()
	tool := NewFileGrepTool(workingDir, config.ToolGrep{})
	input, err := json.Marshal(FileGrepParams{Pattern: pattern, Path: path, Include: include})
	require.NoError(t, err)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Name: FileGrepToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestFileGrep_FindsMatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello world\ngoodbye"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing here"), 0o644))

	resp := runFileGrep(t, dir, "hello", dir, "")

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "a.txt")
	require.NotContains(t, resp.Content, "b.txt")
}

func TestFileGrep_NoMatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing"), 0o644))

	resp := runFileGrep(t, dir, "notfound", dir, "")

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "No files found")
}

func TestFileGrep_IncludeFilter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello"), 0o644))

	resp := runFileGrep(t, dir, "hello", dir, "*.go")

	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "a.go")
	require.NotContains(t, resp.Content, "b.txt")
}

func TestFileGrep_MissingPattern(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileGrep(t, dir, "", dir, "")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "pattern is required")
}

func TestFileGrep_DefaultsToWorkingDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.txt"), []byte("findme"), 0o644))

	// Pass empty path — should default to workingDir (dir).
	tool := NewFileGrepTool(dir, config.ToolGrep{})
	input, err := json.Marshal(FileGrepParams{Pattern: "findme"})
	require.NoError(t, err)
	resp, err := tool.Run(context.Background(), fantasy.ToolCall{Name: FileGrepToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "c.txt")
}
