package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func runFileWrite(t *testing.T, workingDir, path, content string) fantasy.ToolResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tool := NewFileWriteTool(&mockPermissionService{}, workingDir)
	input, err := json.Marshal(FileWriteParams{Path: path, Content: content})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test", Name: FileWriteToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestFileWrite_CreatesNewFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileWrite(t, dir, "hello.txt", "hello world")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello world", string(b))
}

func TestFileWrite_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("old"), 0o644))

	resp := runFileWrite(t, dir, "f.txt", "new content")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "new content", string(b))
}

func TestFileWrite_CreatesParentDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileWrite(t, dir, "a/b/c/file.txt", "nested")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "a", "b", "c", "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "nested", string(b))
}

func TestFileWrite_EmptyContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileWrite(t, dir, "empty.txt", "")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "empty.txt"))
	require.NoError(t, err)
	require.Empty(t, b)
}

func TestFileWrite_MissingPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileWrite(t, dir, "", "content")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "path is required")
}
