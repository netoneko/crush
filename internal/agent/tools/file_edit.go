package tools

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
)

//go:embed file_edit.md
var fileEditDescription string

const FileEditToolName = "file_edit"

type FileEditParams struct {
	Path   string `json:"path" description:"File path to edit (relative to working directory or absolute)"`
	OldStr string `json:"old_str" description:"Exact text to find in the file (must match exactly including whitespace)"`
	NewStr string `json:"new_str" description:"Replacement text (may be empty to delete old_str)"`
}

type fileEditPermissionsParams struct {
	Path   string `json:"path"`
	OldStr string `json:"old_str,omitempty"`
	NewStr string `json:"new_str,omitempty"`
}

func NewFileEditTool(permissions permission.Service, workingDir string) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		FileEditToolName,
		fileEditDescription,
		func(ctx context.Context, params FileEditParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Path == "" {
				return fantasy.NewTextErrorResponse("path is required"), nil
			}
			if params.OldStr == "" {
				return fantasy.NewTextErrorResponse("old_str is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session_id is required")
			}

			filePath := filepathext.SmartJoin(workingDir, params.Path)

			content, err := os.ReadFile(filePath)
			if err != nil {
				if os.IsNotExist(err) {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("file not found: %s", filePath)), nil
				}
				return fantasy.ToolResponse{}, fmt.Errorf("error reading file: %w", err)
			}

			original := string(content)
			if !strings.Contains(original, params.OldStr) {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("old_str not found in %s", filePath)), nil
			}

			modified := strings.Replace(original, params.OldStr, params.NewStr, 1)

			p, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        fsext.PathOrPrefix(filePath, workingDir),
					ToolCallID:  call.ID,
					ToolName:    FileEditToolName,
					Action:      "edit",
					Description: fmt.Sprintf("Edit file %s", filePath),
					Params: fileEditPermissionsParams{
						Path:   filePath,
						OldStr: params.OldStr,
						NewStr: params.NewStr,
					},
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				return NewPermissionDeniedResponse(), nil
			}

			if err := os.WriteFile(filePath, []byte(modified), 0o644); err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error writing file: %w", err)
			}

			oldLines := strings.Count(params.OldStr, "\n") + 1
			newLines := strings.Count(params.NewStr, "\n") + 1
			return fantasy.NewTextResponse(fmt.Sprintf("Edited %s: replaced %d line(s) with %d line(s)", filePath, oldLines, newLines)), nil
		},
	)
}
