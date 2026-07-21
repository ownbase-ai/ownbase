package reconcile_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/runtime"
	"github.com/ownbase/ownbase/internal/schema"
)

func testDesiredJobs(t *testing.T) compiler.RuntimeOutput {
	t.Helper()
	cfg, err := schema.ParseConfigFile("../../testdata/valid/jobs.yaml")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return compiler.Compile(compiler.Input{Config: cfg})
}

const jobContainerUnit = "ownbase-job-nightly-ingest.container"
const jobTimerUnit = "ownbase-job-nightly-ingest.timer"

// findAction returns the first action targeting unitFile, or nil.
func findAction(plan reconcile.Plan, unitFile string) *reconcile.PlannedAction {
	for i := range plan.Actions {
		if plan.Actions[i].UnitFilename == unitFile {
			return &plan.Actions[i]
		}
	}
	return nil
}

// TestDiff_JobContainer_AlreadyInstalledAndNotRunning_NoAction is the core
// regression this feature exists to prevent: a job container is expected to
// be "not running" almost all the time between timer activations, so that
// must never be read as "needs a start" the way it is for a long-running
// service — otherwise every single reconcile tick would plan (and execute) a
// service.start for it.
func TestDiff_JobContainer_AlreadyInstalledAndNotRunning_NoAction(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState() // nothing running, including the job

	currentUnits := map[string]string{
		jobContainerUnit: desired.QuadletUnits[jobContainerUnit], // already installed, unchanged
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if a := findAction(plan, jobContainerUnit); a != nil {
		t.Errorf("expected no action for an already-installed, unchanged job container, got %+v", a)
	}
}

// TestDiff_JobContainer_MissingUnitPlansInstall verifies that a job
// container with no CurrentUnits entry (never installed) is planned an
// install — via ActionServiceStart, but described as an install rather than
// a container start.
func TestDiff_JobContainer_MissingUnitPlansInstall(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: map[string]string{}})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	a := findAction(plan, jobContainerUnit)
	if a == nil {
		t.Fatal("expected an action installing the job container")
	}
	if a.Action.Type != schema.ActionServiceStart {
		t.Errorf("expected ActionServiceStart, got %q", a.Action.Type)
	}
	if !strings.Contains(a.Description, "install job container") {
		t.Errorf("description %q should say \"install job container\"", a.Description)
	}
}

// TestDiff_JobContainer_NoCurrentUnitsContextPlansInstall verifies the
// dev/CI degrade path: when opts.CurrentUnits is nil (no on-disk context
// available at all), a job container is always (re)installed rather than
// silently skipped.
func TestDiff_JobContainer_NoCurrentUnitsContextPlansInstall(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if findAction(plan, jobContainerUnit) == nil {
		t.Error("expected an install action for the job container when CurrentUnits is nil")
	}
}

// TestDiff_JobContainer_ContentChangedPlansReinstall verifies that a changed
// job unit (e.g. new env:/command:) is planned a reinstall via
// ActionServiceRestart — never a "not running -> start".
func TestDiff_JobContainer_ContentChangedPlansReinstall(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	currentUnits := map[string]string{
		jobContainerUnit: desired.QuadletUnits[jobContainerUnit] + "\n# stale\n",
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	a := findAction(plan, jobContainerUnit)
	if a == nil {
		t.Fatal("expected a reinstall action for the changed job container")
	}
	if a.Action.Type != schema.ActionServiceRestart {
		t.Errorf("expected ActionServiceRestart, got %q", a.Action.Type)
	}
	if !strings.Contains(a.Description, "reinstall job container") {
		t.Errorf("description %q should say \"reinstall job container\"", a.Description)
	}
}

// TestDiff_JobContainer_RunningDuringDiff_StillNoStart verifies that even if
// a job container happens to be caught mid-run (present in
// RunningContainers) at diff time, reconcile still never plans a start/
// restart for it based on that fact alone — only content changes do.
func TestDiff_JobContainer_RunningDuringDiff_StillNoStart(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.FakeCurrentState([]string{"ownbase-job-nightly-ingest"})

	currentUnits := map[string]string{
		jobContainerUnit: desired.QuadletUnits[jobContainerUnit],
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if a := findAction(plan, jobContainerUnit); a != nil {
		t.Errorf("expected no action for a mid-run, unchanged job container, got %+v", a)
	}
}

// TestDiff_JobContainer_RemovedFromConfigPlansRemoval verifies that a job
// container left installed on disk after its jobs: entry is deleted gets
// cleaned up, even though it is (as always) absent from RunningContainers.
func TestDiff_JobContainer_RemovedFromConfigPlansRemoval(t *testing.T) {
	desired := testDesired(t) // minimal fixture — no jobs: at all
	current := runtime.EmptyCurrentState()

	currentUnits := map[string]string{
		jobContainerUnit: "leftover content",
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	a := findAction(plan, jobContainerUnit)
	if a == nil {
		t.Fatal("expected a removal action for the orphaned job container")
	}
	if a.Action.Type != schema.ActionServiceStop {
		t.Errorf("expected ActionServiceStop, got %q", a.Action.Type)
	}
}

// TestDiff_Timer_MissingPlansInstallEnable verifies that a job's timer with
// no CurrentUnits entry is planned an install (ActionServiceStart).
func TestDiff_Timer_MissingPlansInstallEnable(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: map[string]string{}})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	a := findAction(plan, jobTimerUnit)
	if a == nil {
		t.Fatal("expected an install action for the timer")
	}
	if a.Action.Type != schema.ActionServiceStart {
		t.Errorf("expected ActionServiceStart, got %q", a.Action.Type)
	}
	if !strings.Contains(a.Description, "enable") {
		t.Errorf("description %q should mention enabling the timer", a.Description)
	}
}

// TestDiff_Timer_AlreadyInstalledUnchanged_NoAction verifies a converged
// timer produces no action — "not running" has no meaning for a timer, so
// this must be purely content-based, like networks/volumes.
func TestDiff_Timer_AlreadyInstalledUnchanged_NoAction(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	currentUnits := map[string]string{
		jobTimerUnit: desired.QuadletUnits[jobTimerUnit],
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if a := findAction(plan, jobTimerUnit); a != nil {
		t.Errorf("expected no action for an unchanged, already-installed timer, got %+v", a)
	}
}

// TestDiff_Timer_ContentChangedPlansRestart verifies that a changed schedule
// triggers a restart so the new OnCalendar takes effect immediately.
func TestDiff_Timer_ContentChangedPlansRestart(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	currentUnits := map[string]string{
		jobTimerUnit: desired.QuadletUnits[jobTimerUnit] + "\n# stale\n",
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	a := findAction(plan, jobTimerUnit)
	if a == nil {
		t.Fatal("expected a restart action for the changed timer")
	}
	if a.Action.Type != schema.ActionServiceRestart {
		t.Errorf("expected ActionServiceRestart, got %q", a.Action.Type)
	}
	if !strings.Contains(a.Description, "schedule changed") {
		t.Errorf("description %q should mention the schedule change", a.Description)
	}
}

// TestDiff_Timer_RemovedFromConfigPlansRemoval verifies that a timer left
// installed on disk after its job is deleted from ownbase.yaml gets
// disabled and removed.
func TestDiff_Timer_RemovedFromConfigPlansRemoval(t *testing.T) {
	desired := testDesired(t) // minimal fixture — no jobs: at all
	current := runtime.EmptyCurrentState()

	currentUnits := map[string]string{
		jobTimerUnit: "leftover content",
	}

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: currentUnits})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	a := findAction(plan, jobTimerUnit)
	if a == nil {
		t.Fatal("expected a removal action for the orphaned timer")
	}
	if a.Action.Type != schema.ActionServiceStop {
		t.Errorf("expected ActionServiceStop, got %q", a.Action.Type)
	}
}

// TestDiff_JobAndTimer_BothInstalledOnConverge verifies that a from-scratch
// diff against jobs.yaml plans installs for both the job container and its
// timer (in addition to the referenced service's own container/network).
func TestDiff_JobAndTimer_BothInstalledOnConverge(t *testing.T) {
	desired := testDesiredJobs(t)
	current := runtime.EmptyCurrentState()

	plan, err := reconcile.Diff(desired, current, reconcile.DiffOptions{CurrentUnits: map[string]string{}})
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if findAction(plan, jobContainerUnit) == nil {
		t.Error("expected an install action for the job container")
	}
	if findAction(plan, jobTimerUnit) == nil {
		t.Error("expected an install action for the timer")
	}
	if findAction(plan, "ownbase-job-cleanup.container") == nil {
		t.Error("expected an install action for the second job's container")
	}
	if findAction(plan, "ownbase-job-cleanup.timer") == nil {
		t.Error("expected an install action for the second job's timer")
	}
}
