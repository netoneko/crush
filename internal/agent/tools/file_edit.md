Make a targeted find-and-replace edit to a file. Use this tool — not bash — whenever you need to modify part of an existing file. Replaces the first occurrence of old_str with new_str.

Parameters:
- **path**: File path to edit. Relative paths are resolved from the working directory.
- **old_str**: Exact text to find in the file. Must match exactly including whitespace.
- **new_str**: Replacement text. May be empty to delete old_str.
