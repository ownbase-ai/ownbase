package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/runtime"
	"github.com/ownbase/ownbase/internal/schema"
)

func newPlanCmd() *cobra.Command {
	var repoDir, fakeCurrent string
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Show what reconcile would do without doing it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(repoDir, fakeCurrent)
		},
	}
	cmd.Flags().StringVar(&repoDir, "dir", ".", "path to the repo checkout")
	cmd.Flags().StringVar(&fakeCurrent, "fake-current", "", "comma-separated list of running container names (empty = treat machine as empty)")
	return cmd
}

// runPlan always compiles into a throwaway temp directory (never --out) so
// that plan is side-effect-free on the real runtime/ — there is nothing for
// an --out flag to control.
func runPlan(repoDir, fakeCurrent string) error {
	cfgPath := filepath.Join(repoDir, "ownbase.yaml")
	cfg, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}

	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	desired := compiler.Compile(compiler.Input{Config: cfg})

	// Read whatever Caddyfile snapshot already exists in repoDir's runtime/
	// before writing to the temp dir below — see readCaddyfileSnapshot.
	currentCaddyfile, caddyfileSnapshotAvailable := readCaddyfileSnapshot(repoDir)

	// Write to a temp dir so plan is side-effect-free on the real runtime/.
	tmpDir, err := os.MkdirTemp("", "ownbasectl-plan-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	if _, err := compiler.WriteOutput(desired, tmpDir); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	var current runtime.CurrentState
	if fakeCurrent != "" {
		parts := strings.Split(fakeCurrent, ",")
		current = runtime.FakeCurrentState(parts)
	} else {
		current = runtime.EmptyCurrentState()
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{
		CurrentCaddyfile:           currentCaddyfile,
		CaddyfileSnapshotAvailable: caddyfileSnapshotAvailable,
	})
	if err != nil {
		return fmt.Errorf("diff: %w", err)
	}

	fmt.Print(reconcile.RenderPlanText(plan))
	return nil
}
