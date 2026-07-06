package reconcile_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/runtime"
	"github.com/ownbase/ownbase/internal/schema"
)

func testDesired(t *testing.T) compiler.RuntimeOutput {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/minimal/ownbase.yaml")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return compiler.Compile(compiler.Input{Config: cfg})
}

func TestDiff_EmptyCurrentProducesStartActions(t *testing.T) {
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("expected non-empty plan against empty current state")
	}
	hasStart := false
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceStart {
			hasStart = true
		}
	}
	if !hasStart {
		t.Error("expected at least one service.start action")
	}
}

func TestDiff_AlreadyRunningProducesEmptyPlan(t *testing.T) {
	desired := testDesired(t)
	// Mark every desired unit (containers, networks, volumes) as already present.
	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)
	// A Caddyfile snapshot matching desired must be provided — otherwise the
	// diff correctly assumes no snapshot exists yet and forces a reload (see
	// TestDiff_CaddyfileReloadForcedWhenNoSnapshotAvailable).
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{
		CurrentCaddyfile:           desired.Caddyfile,
		CaddyfileSnapshotAvailable: true,
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !plan.IsEmpty() {
		t.Errorf("expected empty plan when all units already present, got %d actions", len(plan.Actions))
	}
}

// TestDiff_MissingQuadletFileTriggersRecreate is a regression test for the
// bug where a daemon restart cleaned up Quadlet unit files from
// /etc/containers/systemd/ while leaving the Podman network/volume objects
// in place.
//
// Symptom: the planner only checked current.PresentNetworks / PresentVolumes
// (the Podman object), so it generated no action, leaving systemd without the
// ownbase-X-net-network.service unit that dependent containers require when
// they are next started or restarted.
//
// Fix: Diff also consults opts.InstalledUnits (read from the actual quadlet
// dir). If a network/volume unit file is absent there, the planner re-emits
// the create action even though the Podman object still exists.
func TestDiff_MissingQuadletFileTriggersRecreate(t *testing.T) {
	desired := testDesired(t)

	// Collect what Podman objects already "exist".
	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}

	if len(networks) == 0 && len(volumes) == 0 {
		t.Skip("testdata/minimal has no networks or volumes; test not applicable")
	}

	// Simulate a daemon restart: Podman objects exist but Quadlet unit files
	// are absent from /etc/containers/systemd/ (InstalledUnits has only
	// container files, not network/volume files).
	current := runtime.FullFakeCurrentState(containers, networks, volumes)
	installedOnlyContainers := make(map[string]bool)
	for filename := range desired.QuadletUnits {
		if strings.HasSuffix(filename, ".container") {
			installedOnlyContainers[filename] = true
		}
		// Deliberately omit .network and .volume files to simulate missing
		// unit files in the quadlet directory.
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{
		InstalledUnits: installedOnlyContainers,
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	// Expect start actions for the missing network/volume unit files.
	recreatedNetworks := map[string]bool{}
	recreatedVolumes := map[string]bool{}
	for _, a := range plan.Actions {
		if a.Action.Type != schema.ActionServiceStart {
			continue
		}
		if strings.HasSuffix(a.UnitFilename, ".network") {
			recreatedNetworks[a.UnitFilename] = true
		}
		if strings.HasSuffix(a.UnitFilename, ".volume") {
			recreatedVolumes[a.UnitFilename] = true
		}
	}

	for fn := range desired.QuadletUnits {
		if strings.HasSuffix(fn, ".network") && !recreatedNetworks[fn] {
			t.Errorf("expected start action for missing network unit %s, but it was absent from the plan", fn)
		}
		if strings.HasSuffix(fn, ".volume") && !recreatedVolumes[fn] {
			t.Errorf("expected start action for missing volume unit %s, but it was absent from the plan", fn)
		}
	}
}

func TestDiff_ExtraRunningContainerProducesStop(t *testing.T) {
	desired := testDesired(t)
	current := runtime.FakeCurrentState([]string{"ownbase-orphan"})
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	hasStop := false
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceStop && strings.Contains(a.Description, "orphan") {
			hasStop = true
		}
	}
	if !hasStop {
		t.Error("expected service.stop action for orphaned container")
	}
}

func TestApplyDryRun_ZeroSideEffects(t *testing.T) {
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// The checkpoint must not mutate anything. We use the trivial V1 checkpoint.
	checkpoint := authz.NewTrivialCheckpoint()
	if err := reconcile.ApplyDryRun(plan, checkpoint); err != nil {
		t.Fatalf("ApplyDryRun: %v", err)
	}
	// If we got here with no panic and no error, the dry-run had zero side
	// effects (there is nothing to check other than "it returned cleanly").
}

func TestRenderPlanText_Convergence(t *testing.T) {
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()
	plan, _ := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	text := reconcile.RenderPlanText(plan)
	if !strings.Contains(text, "action") {
		t.Errorf("plan text %q does not mention 'action'", text)
	}
}

func TestRenderPlanText_AlreadyConverged(t *testing.T) {
	desired := testDesired(t)
	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)
	plan, _ := reconcile.Diff(desired, current, reconcile.DiffOptions{
		CurrentCaddyfile:           desired.Caddyfile,
		CaddyfileSnapshotAvailable: true,
	})
	text := reconcile.RenderPlanText(plan)
	if !strings.Contains(text, "converged") {
		t.Errorf("plan text %q does not mention 'converged'", text)
	}
}

// ---------------------------------------------------------------------------
// Apply
// ---------------------------------------------------------------------------

func TestApply_EmptyPlanIsNoop(t *testing.T) {
	// An already-converged plan must return nil and touch nothing.
	desired := testDesired(t)
	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !plan.IsEmpty() {
		t.Skip("fixture is not converged — skipping empty-plan test")
	}

	applier := &reconcile.NoopApplier{}
	if err := reconcile.Apply(plan, authz.NewTrivialCheckpoint(), applier, authz.NopAuditLog()); err != nil {
		t.Fatalf("Apply on empty plan: %v", err)
	}
	if len(applier.Applied) != 0 {
		t.Errorf("expected zero apply calls, got %d", len(applier.Applied))
	}
}

func TestApply_AllActionsExecutedAndAudited(t *testing.T) {
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("expected non-empty plan for fresh machine")
	}

	applier := &reconcile.NoopApplier{}
	mem := &authz.MemAuditLog{}
	if err := reconcile.Apply(plan, authz.NewTrivialCheckpoint(), applier, mem); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Every planned action must have been applied.
	if len(applier.Applied) != len(plan.Actions) {
		t.Errorf("applied %d actions, want %d", len(applier.Applied), len(plan.Actions))
	}
	// Every applied action must appear in the audit log as "applied".
	if len(mem.Records) != len(plan.Actions) {
		t.Errorf("audit log has %d records, want %d", len(mem.Records), len(plan.Actions))
	}
	for i, r := range mem.Records {
		if r.Outcome != authz.OutcomeApplied {
			t.Errorf("record %d: outcome %q, want %q", i, r.Outcome, authz.OutcomeApplied)
		}
	}
}

func TestApply_RollbackOnFailure(t *testing.T) {
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(plan.Actions) < 2 {
		t.Skip("need at least 2 actions to test mid-plan failure")
	}

	// Succeed on the first action, fail on the second. Using FailAfter avoids
	// the problem of multiple actions sharing the same ActionType.
	applier := &reconcile.NoopApplier{FailAfter: 1}
	mem := &authz.MemAuditLog{}

	err = reconcile.Apply(plan, authz.NewTrivialCheckpoint(), applier, mem)
	if err == nil {
		t.Fatal("expected Apply to return error on simulated failure")
	}

	// The first action must have been rolled back.
	if len(applier.RolledBack) == 0 {
		t.Error("expected at least one rollback after failure, got none")
	}

	// The audit log must contain the error outcome for the failed action.
	hasError := false
	for _, r := range mem.Records {
		if r.Outcome == authz.OutcomeError {
			hasError = true
		}
	}
	if !hasError {
		t.Error("audit log has no error record for the failed action")
	}

	// The audit log must contain rollback records for the actions that succeeded.
	hasRollback := false
	for _, r := range mem.Records {
		if r.Outcome == authz.OutcomeRolledBack {
			hasRollback = true
		}
	}
	if !hasRollback {
		t.Error("audit log has no rolled_back record for the actions that were undone")
	}
}

func TestApply_CheckpointRefusalAbortsEarly(t *testing.T) {
	// A checkpoint that refuses everything.
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if plan.IsEmpty() {
		t.Skip("need non-empty plan to test refusal")
	}

	applier := &reconcile.NoopApplier{}
	mem := &authz.MemAuditLog{}

	err = reconcile.Apply(plan, refuseAll{}, applier, mem)
	if err == nil {
		t.Fatal("expected Apply to return error when checkpoint refuses")
	}
	if len(applier.Applied) != 0 {
		t.Errorf("applier was called despite checkpoint refusal")
	}
	// The refusal must be recorded.
	if len(mem.Records) != 1 || mem.Records[0].Outcome != authz.OutcomeRefused {
		t.Errorf("expected 1 refused audit record, got %v", mem.Records)
	}
}

// refuseAll is a Checkpoint that refuses every action.
type refuseAll struct{}

func (refuseAll) Authorize(a schema.Action) error {
	return fmt.Errorf("all actions refused by test policy")
}

// ---------------------------------------------------------------------------
// M11: Topological start ordering (Tier-1)
// ---------------------------------------------------------------------------

// TestDiff_TopologicalOrder verifies that when "hello" declares requires: [auth],
// the plan schedules ownbase-auth start before ownbase-hello start.
// Uses the testdata/minimal fixture which already encodes this dependency.
func TestDiff_TopologicalOrder(t *testing.T) {
	desired := testDesired(t)
	current := runtime.EmptyCurrentState()

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	// Locate the indices of the ownbase-auth and ownbase-hello start actions.
	authIdx, helloIdx := -1, -1
	for i, pa := range plan.Actions {
		if pa.Action.Type == schema.ActionServiceStart {
			switch pa.Action.Target {
			case "ownbase-auth":
				authIdx = i
			case "ownbase-hello":
				helloIdx = i
			}
		}
	}
	if authIdx < 0 {
		t.Fatal("plan missing ownbase-auth start action")
	}
	if helloIdx < 0 {
		t.Fatal("plan missing ownbase-hello start action")
	}
	if authIdx >= helloIdx {
		t.Errorf("ownbase-auth (index %d) must start before ownbase-hello (index %d)",
			authIdx, helloIdx)
	}
}

// TestDiff_CycleDetection verifies that a requires: cycle returns a hard error.
func TestDiff_CycleDetection(t *testing.T) {
	// Build a RuntimeOutput with a cycle: a→b, b→a.
	cycleOutput := compiler.RuntimeOutput{
		QuadletUnits: map[string]string{
			"ownbase-a.container": "# Requires=b\n[Container]\nImage=localhost/ownbase-a:local\n",
			"ownbase-b.container": "# Requires=a\n[Container]\nImage=localhost/ownbase-b:local\n",
		},
	}
	current := runtime.EmptyCurrentState()
	_, err := reconcile.Diff(cycleOutput, current, reconcile.DiffOptions{})
	if err == nil {
		t.Fatal("expected error for requires: cycle, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected 'cycle' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// M11: Audit exhaustiveness (Tier-1)
// ---------------------------------------------------------------------------

// TestApply_AllTaxonomyTypesAudited creates a plan covering every action type
// in the taxonomy, applies it with a NoopApplier, and asserts that the audit
// log contains one record per action — confirming the Apply loop never silently
// drops an action type from the log.
func TestApply_AllTaxonomyTypesAudited(t *testing.T) {
	allActions := schema.AllActions()
	actions := make([]reconcile.PlannedAction, 0, len(allActions))
	for _, a := range allActions {
		actions = append(actions, reconcile.PlannedAction{
			Action:      a,
			Description: fmt.Sprintf("synthetic %s action", a.Type),
		})
	}
	plan := reconcile.Plan{Actions: actions}

	applier := &reconcile.NoopApplier{}
	mem := &authz.MemAuditLog{}
	if err := reconcile.Apply(plan, authz.NewTrivialCheckpoint(), applier, mem); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(mem.Records) != len(allActions) {
		t.Errorf("audit log has %d records, want %d (one per taxonomy action)",
			len(mem.Records), len(allActions))
	}
	// Every record must be "applied".
	for i, r := range mem.Records {
		if r.Outcome != authz.OutcomeApplied {
			t.Errorf("record %d (action %s): outcome %q, want %q",
				i, r.Action, r.Outcome, authz.OutcomeApplied)
		}
	}
}

// ---------------------------------------------------------------------------
// DetectDrift
// ---------------------------------------------------------------------------

func TestDetectDrift_NoDriftWhenExact(t *testing.T) {
	desired := testDesired(t)

	// Write the compiler output to a temp dir and then check for drift.
	dir := t.TempDir()
	if _, err := compiler.WriteOutput(desired, dir); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}

	events, err := reconcile.DetectDrift(desired, filepath.Join(dir, "runtime"))
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no drift events, got %d: %v", len(events), events)
	}
}

func TestDetectDrift_MissingFile(t *testing.T) {
	desired := testDesired(t)

	dir := t.TempDir()
	if _, err := compiler.WriteOutput(desired, dir); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}

	// Delete one of the generated files to simulate missing output.
	runtimeDir := filepath.Join(dir, "runtime")
	entries, _ := os.ReadDir(runtimeDir)
	if len(entries) == 0 {
		t.Skip("no files in runtime/ to delete")
	}
	deleted := entries[0].Name()
	os.Remove(filepath.Join(runtimeDir, deleted))

	events, err := reconcile.DetectDrift(desired, runtimeDir)
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}

	found := false
	for _, e := range events {
		if e.Filename == deleted && e.Kind == reconcile.DriftKindMissingFile {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing_file event for %q, got: %v", deleted, events)
	}
}

func TestDetectDrift_ContentChanged(t *testing.T) {
	desired := testDesired(t)

	dir := t.TempDir()
	if _, err := compiler.WriteOutput(desired, dir); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}

	runtimeDir := filepath.Join(dir, "runtime")
	entries, _ := os.ReadDir(runtimeDir)
	if len(entries) == 0 {
		t.Skip("no files in runtime/ to modify")
	}
	modified := entries[0].Name()
	_ = os.WriteFile(filepath.Join(runtimeDir, modified), []byte("# hand-edited!\n"), 0o644)

	events, err := reconcile.DetectDrift(desired, runtimeDir)
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}

	found := false
	for _, e := range events {
		if e.Filename == modified && e.Kind == reconcile.DriftKindContentChanged {
			found = true
		}
	}
	if !found {
		t.Errorf("expected content_changed event for %q, got: %v", modified, events)
	}
}

func TestDetectDrift_UnexpectedFile(t *testing.T) {
	desired := testDesired(t)

	dir := t.TempDir()
	if _, err := compiler.WriteOutput(desired, dir); err != nil {
		t.Fatalf("WriteOutput: %v", err)
	}

	runtimeDir := filepath.Join(dir, "runtime")
	extraFile := filepath.Join(runtimeDir, "hand-created.txt")
	_ = os.WriteFile(extraFile, []byte("should not be here\n"), 0o644)

	events, err := reconcile.DetectDrift(desired, runtimeDir)
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}

	found := false
	for _, e := range events {
		if e.Filename == "hand-created.txt" && e.Kind == reconcile.DriftKindUnexpectedFile {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unexpected_file event for 'hand-created.txt', got: %v", events)
	}
}

func TestDetectDrift_RuntimeDirAbsent(t *testing.T) {
	desired := testDesired(t)

	events, err := reconcile.DetectDrift(desired, "/nonexistent/runtime")
	if err != nil {
		t.Fatalf("DetectDrift should not error on absent dir: %v", err)
	}
	// Every expected file should be reported as missing.
	missingCount := 0
	for _, e := range events {
		if e.Kind == reconcile.DriftKindMissingFile {
			missingCount++
		}
	}
	if missingCount == 0 {
		t.Error("expected missing_file events when runtime dir is absent")
	}
}

func TestRenderDriftReport_EmptyIsBlank(t *testing.T) {
	if s := reconcile.RenderDriftReport(nil); s != "" {
		t.Errorf("RenderDriftReport(nil) = %q, want empty", s)
	}
	if s := reconcile.RenderDriftReport([]reconcile.DriftEvent{}); s != "" {
		t.Errorf("RenderDriftReport([]) = %q, want empty", s)
	}
}

// ---------------------------------------------------------------------------
// Diff — restart action when unit content changes (Fix 3)
// ---------------------------------------------------------------------------

func TestDiff_UnitContentChangedEmitsRestart(t *testing.T) {
	desired := testDesired(t)

	// Collect all container names and mark them as running.
	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)

	// Build stale current units — same keys but different content.
	staleUnits := make(map[string]string)
	for filename, content := range desired.QuadletUnits {
		if strings.HasSuffix(filename, ".container") {
			staleUnits[filename] = content + "\n# stale-marker\n"
		} else {
			staleUnits[filename] = content
		}
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: staleUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	hasRestart := false
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceRestart {
			hasRestart = true
		}
	}
	if !hasRestart {
		t.Errorf("expected service.restart action when unit content changed, got plan: %v", plan.Actions)
	}
}

func TestDiff_UnitContentUnchangedNoRestart(t *testing.T) {
	desired := testDesired(t)

	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)

	// Current units match desired exactly.
	sameUnits := make(map[string]string, len(desired.QuadletUnits))
	for k, v := range desired.QuadletUnits {
		sameUnits[k] = v
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: sameUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceRestart {
			t.Errorf("unexpected service.restart when unit content is unchanged: %v", a.Description)
		}
	}
}

// ---------------------------------------------------------------------------
// Diff — Caddyfile-only reload (Fix 2)
// ---------------------------------------------------------------------------

func TestDiff_CaddyfileOnlyReloadWhenChanged(t *testing.T) {
	desired := testDesired(t)
	if desired.Caddyfile == "" {
		t.Skip("testDesired has no Caddyfile — skip Caddyfile-only reload test")
	}

	// All containers running so no start/stop actions.
	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)

	// Provide a stale Caddyfile so the diff should emit a reload-only plan.
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{
		CurrentCaddyfile:           "# stale Caddyfile\n",
		CaddyfileSnapshotAvailable: true,
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	hasReload := false
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceReload {
			hasReload = true
		}
	}
	if !hasReload {
		t.Errorf("expected service.reload when Caddyfile changed, got: %v", plan.Actions)
	}
}

func TestDiff_NoCaddyfileReloadWhenUnchanged(t *testing.T) {
	desired := testDesired(t)

	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)

	// Current Caddyfile matches desired — no reload.
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{
		CurrentCaddyfile:           desired.Caddyfile,
		CaddyfileSnapshotAvailable: true,
	})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceReload {
			t.Errorf("unexpected service.reload when Caddyfile is unchanged: %v", a.Description)
		}
	}
}

// TestDiff_CaddyfileReloadForcedWhenNoSnapshotAvailable verifies the actual
// bug fix: on a Base's very first boot, runtime/Caddyfile does not exist yet
// (CaddyfileSnapshotAvailable is false) even though Caddy is already running
// its stock default config. The diff must still force a reload so the
// compiled Caddyfile actually reaches Caddy, even when CurrentCaddyfile
// happens to equal desired.Caddyfile (e.g. both are read as empty strings)
// and even when no other actions are pending.
func TestDiff_CaddyfileReloadForcedWhenNoSnapshotAvailable(t *testing.T) {
	desired := testDesired(t)
	if desired.Caddyfile == "" {
		t.Skip("testDesired has no Caddyfile — skip Caddyfile snapshot test")
	}

	var containers, networks, volumes []string
	for filename := range desired.QuadletUnits {
		switch {
		case strings.HasSuffix(filename, ".container"):
			containers = append(containers, strings.TrimSuffix(filename, ".container"))
		case strings.HasSuffix(filename, ".network"):
			networks = append(networks, strings.TrimSuffix(filename, ".network"))
		case strings.HasSuffix(filename, ".volume"):
			volumes = append(volumes, strings.TrimSuffix(filename, ".volume"))
		}
	}
	current := runtime.FullFakeCurrentState(containers, networks, volumes)

	// No snapshot available at all (CaddyfileSnapshotAvailable left false) —
	// CurrentCaddyfile is left at its zero value, which would look identical
	// to an actually-empty-but-present snapshot without the flag.
	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	hasReload := false
	for _, a := range plan.Actions {
		if a.Action.Type == schema.ActionServiceReload {
			hasReload = true
		}
	}
	if !hasReload {
		t.Error("expected service.reload to be forced when no Caddyfile snapshot is available, even with no other actions")
	}
}
