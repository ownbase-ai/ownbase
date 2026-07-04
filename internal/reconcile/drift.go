package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ownbase/ownbase/internal/compiler"
)

// DriftKind classifies a DriftEvent.
const (
	DriftKindMissingFile    = "missing_file"
	DriftKindContentChanged = "content_changed"
	DriftKindUnexpectedFile = "unexpected_file"
)

// DriftEvent describes one file in runtime/ that differs from what the
// compiler produced. Any DriftEvent is a tamper/drift signal — runtime/ has
// a single writer (the compiler, via the agent) and hand-edits are
// explicitly prohibited (Architecture Principle 5).
type DriftEvent struct {
	// Filename is the basename of the affected file in runtime/.
	Filename string
	// Kind is one of the DriftKind* constants.
	Kind string
	// Detail is a human-readable explanation.
	Detail string
}

// DetectDrift compares the expected RuntimeOutput (the compiler's view of
// what runtime/ should contain) against the files actually present at
// runtimeDir. It returns nil when the directory is an exact match.
//
// DetectDrift is deterministic and side-effect-free: it reads files but never
// writes them. Re-running it against an unchanged directory returns the same
// result.
//
// runtimeDir should be the full path to the runtime/ subdirectory
// (e.g. /opt/ownbase/runtime or <outDir>/runtime in tests).
// If runtimeDir does not exist, all expected files are reported as missing.
func DetectDrift(desired compiler.RuntimeOutput, runtimeDir string) ([]DriftEvent, error) {
	// Build the expected file map from compiler output.
	expected := buildExpectedFiles(desired)

	var events []DriftEvent

	// Check that every expected file is present and matches content.
	for _, filename := range sortedKeys(expected) {
		expectedContent := expected[filename]
		path := filepath.Join(runtimeDir, filename)
		actual, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			events = append(events, DriftEvent{
				Filename: filename,
				Kind:     DriftKindMissingFile,
				Detail:   "file expected by compiler is absent from runtime/",
			})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("drift: read %s: %w", path, err)
		}
		if string(actual) != expectedContent {
			events = append(events, DriftEvent{
				Filename: filename,
				Kind:     DriftKindContentChanged,
				Detail:   "file content differs from compiler output (possible hand-edit or external write)",
			})
		}
	}

	// Check for files in runtime/ that the compiler did not produce.
	entries, err := os.ReadDir(runtimeDir)
	if os.IsNotExist(err) {
		// Directory absent — missing-file events already recorded above.
		return events, nil
	}
	if err != nil {
		return nil, fmt.Errorf("drift: readdir %s: %w", runtimeDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := expected[entry.Name()]; !ok {
			events = append(events, DriftEvent{
				Filename: entry.Name(),
				Kind:     DriftKindUnexpectedFile,
				Detail:   "file present in runtime/ but not produced by the compiler",
			})
		}
	}

	// Sort for deterministic output.
	sort.Slice(events, func(i, j int) bool {
		if events[i].Filename != events[j].Filename {
			return events[i].Filename < events[j].Filename
		}
		return events[i].Kind < events[j].Kind
	})

	return events, nil
}

// RenderDriftReport returns a human-readable summary of drift events.
// Returns an empty string when events is nil/empty.
func RenderDriftReport(events []DriftEvent) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "drift: %d event(s) detected in runtime/\n", len(events))
	for _, e := range events {
		fmt.Fprintf(&b, "  [%s] %s — %s\n", e.Kind, e.Filename, e.Detail)
	}
	return b.String()
}

// buildExpectedFiles assembles the set of filenames and their expected
// content from a RuntimeOutput.
func buildExpectedFiles(out compiler.RuntimeOutput) map[string]string {
	m := make(map[string]string, len(out.QuadletUnits)+2)
	for filename, content := range out.QuadletUnits {
		m[filename] = content
	}
	if out.Caddyfile != "" {
		m["Caddyfile"] = out.Caddyfile
	}
	if out.ComposeFile != "" {
		m["docker-compose.yml"] = out.ComposeFile
	}
	return m
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
