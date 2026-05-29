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

func runFileEdit(t *testing.T, workingDir, path, oldStr, newStr string) fantasy.ToolResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	tool := NewFileEditTool(&mockPermissionService{}, workingDir)
	input, err := json.Marshal(FileEditParams{Path: path, OldStr: oldStr, NewStr: newStr})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test", Name: FileEditToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func TestFileEdit_ReplacesFirstOccurrence(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("aaa bbb aaa"), 0o644))

	resp := runFileEdit(t, dir, "f.txt", "aaa", "ccc")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "ccc bbb aaa", string(b))
}

func TestFileEdit_DeletesText(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello world"), 0o644))

	resp := runFileEdit(t, dir, "f.txt", " world", "")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(b))
}

func TestFileEdit_MultilineReplacement(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := "line1\nline2\nline3\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte(original), 0o644))

	resp := runFileEdit(t, dir, "f.txt", "line1\nline2", "replaced")

	require.False(t, resp.IsError)
	b, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "replaced\nline3\n", string(b))
}

func TestFileEdit_OldStrNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0o644))

	resp := runFileEdit(t, dir, "f.txt", "nothere", "x")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "old_str not found")
}

func TestFileEdit_FileNotFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileEdit(t, dir, "missing.txt", "x", "y")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "file not found")
}

func TestFileEdit_MissingPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	resp := runFileEdit(t, dir, "", "x", "y")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "path is required")
}

func TestFileEdit_MissingOldStr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0o644))

	resp := runFileEdit(t, dir, "f.txt", "", "y")

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "old_str is required")
}
