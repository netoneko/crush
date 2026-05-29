package tools

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed file_grep.md
var fileGrepDescription string

const FileGrepToolName = "file_grep"

type FileGrepParams struct {
	Pattern string `json:"pattern" description:"Regular expression to search for in file contents"`
	Path    string `json:"path,omitempty" description:"Directory or file to search. Defaults to the working directory."`
	Include string `json:"include,omitempty" description:"Glob pattern to filter which files are searched (e.g. *.go, *.{ts,tsx})"`
}

func NewFileGrepTool(workingDir string, cfg config.ToolGrep) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		FileGrepToolName,
		fileGrepDescription,
		func(ctx context.Context, params FileGrepParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			searchPath := params.Path
			if searchPath == "" {
				searchPath = workingDir
			}

			searchCtx, cancel := context.WithTimeout(ctx, cfg.GetTimeout())
			defer cancel()

			matches, truncated, err := searchFiles(searchCtx, params.Pattern, searchPath, params.Include, 100)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error searching files: %v", err)), nil
			}

			var output strings.Builder
			if len(matches) == 0 {
				output.WriteString("No files found")
			} else {
				fmt.Fprintf(&output, "Found %d matches\n", len(matches))
				currentFile := ""
				for _, match := range matches {
					if currentFile != match.path {
						if currentFile != "" {
							output.WriteString("\n")
						}
						currentFile = match.path
						fmt.Fprintf(&output, "%s:\n", filepath.ToSlash(match.path))
					}
					if match.lineNum > 0 {
						lineText := match.lineText
						if len(lineText) > maxGrepContentWidth {
							lineText = lineText[:maxGrepContentWidth] + "..."
						}
						fmt.Fprintf(&output, "  Line %d: %s\n", match.lineNum, lineText)
					}
				}
				if truncated {
					output.WriteString("\n(Results are truncated. Consider using a more specific path or pattern.)")
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output.String()),
				GrepResponseMetadata{
					NumberOfMatches: len(matches),
					Truncated:       truncated,
				},
			), nil
		},
	)
}
