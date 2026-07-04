package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/schema"
)

func newCompileCmd() *cobra.Command {
	var repoDir, outDir string
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile ownbase.yaml + manifests to runtime/ files",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCompile(repoDir, outDir)
		},
	}
	cmd.Flags().StringVar(&repoDir, "dir", ".", "path to the repo checkout (directory containing ownbase.yaml)")
	cmd.Flags().StringVar(&outDir, "out", "", "output directory (default: <repo>)")
	return cmd
}

func runCompile(repoDir, outDir string) error {
	if outDir == "" {
		outDir = repoDir
	}

	cfgPath := filepath.Join(repoDir, "ownbase.yaml")
	cfg, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}

	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	out := compiler.Compile(compiler.Input{Config: cfg})

	written, err := compiler.WriteOutput(out, outDir)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	fmt.Printf("compile: wrote %d file(s) to %s/runtime/\n", len(written), outDir)
	for _, f := range written {
		fmt.Printf("  %s\n", f)
	}
	return nil
}
