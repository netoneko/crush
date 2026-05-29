Write content to a file. Use this tool — not bash — whenever you need to create or overwrite a file. Accepts structured path and content fields so no shell escaping is needed.

Parameters:
- **path**: File path to write. Relative paths are resolved from the working directory.
- **content**: Full content to write to the file. Parent directories are created automatically.
