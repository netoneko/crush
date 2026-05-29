package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed file_write.md
var fileWriteDescription string

const FileWriteToolName = "file_write"

type FileWriteParams struct {
	Path    string `json:"path" description:"File path to write (relative to working directory or absolute)"`
	Content string `json:"content" description:"Full content to write to the file"`
}

type fileWritePermissionsParams struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

func NewFileWriteTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		FileWriteToolName,
		fileWriteDescription,
		func(ctx context.Context, params FileWriteParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Path == "" {
				return fantasy.NewTextErrorResponse("path is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session_id is required")
			}

			filePath := filepathext.SmartJoin(workingDir, params.Path)

			p, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        fsext.PathOrPrefix(filePath, workingDir),
					ToolCallID:  call.ID,
					ToolName:    FileWriteToolName,
					Action:      "write",
					Description: fmt.Sprintf("Write file %s", filePath),
					Params: fileWritePermissionsParams{
						Path:    filePath,
						Content: params.Content,
					},
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				return NewPermissionDeniedResponse(), nil
			}

			if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error creating directory: %w", err)
			}

			if err := os.WriteFile(filePath, []byte(params.Content), 0o644); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error writing file: %w", err)
			}

			return fantasy.NewTextResponse(fmt.Sprintf("Written %d bytes to %s", len(params.Content), filePath)), nil
		},
	)
}
