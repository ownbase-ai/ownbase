package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/runtime"
	"github.com/ownbase/ownbase/internal/schema"
)

func newApplyCmd() *cobra.Command {
	var (
		repoDir      string
		dryRun       bool
		fakeCurrent  string
		auditLogPath string
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply the reconcile plan (use --dry-run for a preview)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(repoDir, dryRun, fakeCurrent, auditLogPath)
		},
	}
	cmd.Flags().StringVar(&repoDir, "dir", ".", "path to the repo checkout")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be done without doing anything")
	cmd.Flags().StringVar(&fakeCurrent, "fake-current", "", "comma-separated list of running container names")
	cmd.Flags().StringVar(&auditLogPath, "audit-log", authz.DefaultAuditLogPath,
		"path to the audit log file (only used without --dry-run)")
	return cmd
}

func runApply(repoDir string, dryRun bool, fakeCurrent, auditLogPath string) error {
	cfgPath := filepath.Join(repoDir, "ownbase.yaml")
	cfg, err := schema.ParseConfigFile(cfgPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}

	for _, w := range cfg.Warnings() {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	desired := compiler.Compile(compiler.Input{Config: cfg})

	var current runtime.CurrentState
	if fakeCurrent != "" {
		parts := strings.Split(fakeCurrent, ",")
		current = runtime.FakeCurrentState(parts)
	} else {
		current = runtime.EmptyCurrentState()
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		return fmt.Errorf("diff: %w", err)
	}

	if plan.IsEmpty() {
		fmt.Println("apply: no changes — already converged.")
		return nil
	}

	checkpoint := authz.NewTrivialCheckpoint()

	if dryRun {
		fmt.Printf("apply --dry-run: %d action(s) (no side effects)\n", len(plan.Actions))
		return reconcile.ApplyDryRun(plan, checkpoint)
	}

	// Real apply. Audit log captures every action.
	// M3 Tier-2: replace NoopApplier with a real podman/systemd applier.
	// Until then, apply runs through the full transactional path (checkpoint →
	// apply → audit) but does not touch the system.
	applier := &reconcile.NoopApplier{}
	auditLog, err := authz.NewAuditLog(auditLogPath)
	if err != nil {
		return fmt.Errorf("open audit log %s: %w", auditLogPath, err)
	}
	defer auditLog.Close()

	fmt.Printf("apply: %d action(s) — audit log: %s\n", len(plan.Actions), auditLog.Path())
	fmt.Println("  note: real system apply requires Ubuntu + Podman (M3 Tier-2)")
	fmt.Print(reconcile.RenderPlanText(plan))

	return reconcile.Apply(plan, checkpoint, applier, auditLog)
}
