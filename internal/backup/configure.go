package backup

// configure.go provides a text-preserving edit of the core.backup: block in
// ownbase.yaml, used by the daemon's /backup/configure API so that
// `ownbasectl backup setup` can turn on backups without the customer
// hand-editing YAML. Follows the same line-surgery approach as
// internal/update.BumpRef so unrelated formatting and comments in the rest
// of the file are left untouched.

import "strings"

// SetCoreBackupConfig sets repo (required) and, when non-empty, interval and
// verifyInterval within the core.backup: block of yamlContent. Creates the
// core: and/or backup: blocks if they do not already exist. Existing fields
// are replaced in place; new fields are appended to the end of the backup:
// block.
func SetCoreBackupConfig(yamlContent, repo, interval, verifyInterval string) string {
	lines := strings.Split(yamlContent, "\n")

	coreIdx := findTopLevelKey(lines, "core:")
	if coreIdx == -1 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "core:")
		coreIdx = len(lines) - 1
	}
	coreIndent := indentOf(lines[coreIdx])
	coreStart, coreEnd := blockRange(lines, coreIdx, coreIndent)

	backupIndent := coreIndent + 2
	backupIdx := findChildKey(lines, coreStart, coreEnd, backupIndent, "backup:")
	if backupIdx == -1 {
		lines = insertLines(lines, coreEnd, []string{strings.Repeat(" ", backupIndent) + "backup:"})
		backupIdx = coreEnd
	}
	backupStart, backupEnd := blockRange(lines, backupIdx, backupIndent)

	fieldIndent := backupIndent + 2
	lines, backupEnd = setOrInsertField(lines, backupStart, backupEnd, fieldIndent, "repo", repo)
	if interval != "" {
		lines, backupEnd = setOrInsertField(lines, backupStart, backupEnd, fieldIndent, "interval", interval)
	}
	if verifyInterval != "" {
		lines, _ = setOrInsertField(lines, backupStart, backupEnd, fieldIndent, "verify_interval", verifyInterval)
	}

	return strings.Join(lines, "\n")
}

// findTopLevelKey returns the index of the first line beginning with key at
// zero indent, or -1 when absent.
func findTopLevelKey(lines []string, key string) int {
	for i, line := range lines {
		if strings.HasPrefix(line, key) {
			return i
		}
	}
	return -1
}

// findChildKey returns the index of the first line within [start, end) that
// is exactly key at the given indent, or -1 when absent.
func findChildKey(lines []string, start, end, indent int, key string) int {
	prefix := strings.Repeat(" ", indent) + key
	for i := start; i < end; i++ {
		if strings.HasPrefix(lines[i], prefix) {
			return i
		}
	}
	return -1
}

// blockRange returns the [start, end) line range of the nested block that
// begins on the line after headerIdx, where headerIdx is a "key:" line at
// headerIndent. end is the index of the first following line with indent
// <= headerIndent (a sibling key or the end of file); blank lines and
// comments are skipped when determining the boundary.
func blockRange(lines []string, headerIdx, headerIndent int) (start, end int) {
	start = headerIdx + 1
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indentOf(lines[i]) <= headerIndent {
			return start, i
		}
	}
	return start, len(lines)
}

// setOrInsertField replaces the "key: value" line within [start, end) at the
// given indent, or appends one at position end when absent. Returns the
// updated lines and the new end-of-block index.
func setOrInsertField(lines []string, start, end, indent int, key, value string) ([]string, int) {
	idx := findChildKey(lines, start, end, indent, key+":")
	newLine := strings.Repeat(" ", indent) + key + ": " + value
	if idx != -1 {
		lines[idx] = newLine
		return lines, end
	}
	lines = insertLines(lines, end, []string{newLine})
	return lines, end + 1
}

// insertLines splits lines at index at and splices newLines in between.
func insertLines(lines []string, at int, newLines []string) []string {
	out := make([]string, 0, len(lines)+len(newLines))
	out = append(out, lines[:at]...)
	out = append(out, newLines...)
	out = append(out, lines[at:]...)
	return out
}

// indentOf returns the number of leading spaces on line.
func indentOf(line string) int {
	trimmed := strings.TrimLeft(line, " ")
	return len(line) - len(trimmed)
}
