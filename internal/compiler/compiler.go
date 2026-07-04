// Package compiler turns an OwnbaseConfig into the runtime artifacts the agent
// applies. The compiler is a pure function:
//
//	compile(config) → RuntimeOutput
//
// No clock, no hostname, no random, no environment reads. The same input
// always produces byte-identical output.
//
// Pipeline:
//
//	Input (config)
//	  → build()     produces RuntimeModel   (typed, in-memory)
//	  → render()    produces RuntimeOutput  (text files)
//	  → WriteOutput writes files to disk
package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ownbase/ownbase/internal/schema"
)

// RuntimeOutput holds the in-memory result of a compile run.
type RuntimeOutput struct {
	// QuadletUnits maps filename → content for all generated systemd unit files.
	QuadletUnits map[string]string
	// Caddyfile is the full generated Caddyfile content.
	Caddyfile string
	// ComposeFile is the Compose export for inspection and portability.
	ComposeFile string
}

// Input is everything the compiler needs to produce RuntimeOutput.
type Input struct {
	Config *schema.OwnbaseConfig
}

// Compile is the pure function at the center of the model.
func Compile(in Input) RuntimeOutput {
	model := build(in)
	return render(model)
}

// CompileToModel returns the typed RuntimeModel without rendering to text.
// This is the AI-preview interface: a structured value the user's AI can
// inspect to understand what a commit would change before it is applied.
func CompileToModel(in Input) RuntimeModel {
	return build(in)
}

// WriteOutput writes a RuntimeOutput to outDir under outDir/runtime/.
// Returns the list of files written, sorted for stable output.
func WriteOutput(out RuntimeOutput, outDir string) ([]string, error) {
	runtimeDir := filepath.Join(outDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir runtime: %w", err)
	}

	filenames := make([]string, 0, len(out.QuadletUnits))
	for f := range out.QuadletUnits {
		filenames = append(filenames, f)
	}
	sort.Strings(filenames)

	var written []string
	for _, filename := range filenames {
		path := filepath.Join(runtimeDir, filename)
		if err := os.WriteFile(path, []byte(out.QuadletUnits[filename]), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", filename, err)
		}
		written = append(written, path)
	}

	if out.Caddyfile != "" {
		path := filepath.Join(runtimeDir, "Caddyfile")
		if err := os.WriteFile(path, []byte(out.Caddyfile), 0o644); err != nil {
			return nil, fmt.Errorf("write Caddyfile: %w", err)
		}
		written = append(written, filepath.Join(runtimeDir, "Caddyfile"))
	}

	if out.ComposeFile != "" {
		path := filepath.Join(runtimeDir, "docker-compose.yml")
		if err := os.WriteFile(path, []byte(out.ComposeFile), 0o644); err != nil {
			return nil, fmt.Errorf("write docker-compose.yml: %w", err)
		}
		written = append(written, filepath.Join(runtimeDir, "docker-compose.yml"))
	}

	return written, nil
}
