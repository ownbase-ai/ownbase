package reconcile

import (
	"fmt"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

// Applier executes and undoes planned actions against the real runtime.
// The interface keeps internal/reconcile free of direct runtime imports so
// each layer can be tested independently.
//
// The production implementation lives in cmd/ownbased and wires the
// runtime (podman/systemd) adapters. Tests use NoopApplier.
type Applier interface {
	// ApplyAction executes the action, mutating real system state. Returns
	// nil on success, or a non-nil error if the action could not complete.
	// The Apply function treats any non-nil error as a trigger for rollback.
	ApplyAction(action PlannedAction) error

	// RollbackAction undoes the effect of a previously applied action.
	// Called in reverse order for each successfully applied action when a
	// later action fails. Rollback errors are reported but do not mask the
	// original failure.
	RollbackAction(action PlannedAction) error
}

// Apply executes plan transactionally:
//  1. Each action passes through checkpoint (taxonomy + policy check).
//  2. The action is executed via applier.ApplyAction.
//  3. The outcome (applied / refused / error) is recorded in log.
//  4. On any failure, all successfully applied actions are rolled back in
//     reverse order before returning.
//
// Apply is idempotent when the plan is empty (returns nil immediately).
// The caller is responsible for passing a non-nil checkpoint, applier, and
// log; use authz.NopAuditLog() when audit recording is not required.
func Apply(plan Plan, checkpoint authz.Checkpoint, applier Applier, log authz.AuditLogger) error {
	if plan.IsEmpty() {
		return nil
	}

	applied := make([]PlannedAction, 0, len(plan.Actions))

	for _, pa := range plan.Actions {
		// Authorization checkpoint — taxonomy validation + V1 trivial policy.
		if err := checkpoint.Authorize(pa.Action); err != nil {
			_ = log.Record(pa.Action, authz.OutcomeRefused, err.Error())
			return fmt.Errorf("checkpoint refused %q on %q: %w", pa.Action.Type, pa.Action.Target, err)
		}

		// Execute the action.
		if err := applier.ApplyAction(pa); err != nil {
			_ = log.Record(pa.Action, authz.OutcomeError, err.Error())

			// Rollback all previously applied actions in reverse order.
			rbErr := rollbackApplied(applied, applier, log)
			if rbErr != nil {
				return fmt.Errorf("apply %q on %q failed (%w); rollback also failed: %v",
					pa.Action.Type, pa.Action.Target, err, rbErr)
			}
			return fmt.Errorf("apply %q on %q: %w", pa.Action.Type, pa.Action.Target, err)
		}

		_ = log.Record(pa.Action, authz.OutcomeApplied, "")
		applied = append(applied, pa)
	}
	return nil
}

// rollbackApplied reverses the applied slice in reverse order. It logs each
// result but returns only the first rollback error (subsequent errors are
// still attempted and logged).
func rollbackApplied(applied []PlannedAction, applier Applier, log authz.AuditLogger) error {
	var firstErr error
	for i := len(applied) - 1; i >= 0; i-- {
		pa := applied[i]
		if err := applier.RollbackAction(pa); err != nil {
			_ = log.Record(pa.Action, authz.OutcomeRollbackError, err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_ = log.Record(pa.Action, authz.OutcomeRolledBack, "")
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// NoopApplier
// ---------------------------------------------------------------------------

// NoopApplier records the calls it would make without touching the system.
// Use it in dry-run paths, unit tests, and any context where the real
// podman/systemd runtime is not available.
//
// Failure simulation (pick at most one):
//   - FailOn: fail when the action type matches this value.
//   - FailAfter: succeed the first N calls, then fail; 0 = never fail this way.
type NoopApplier struct {
	Applied    []PlannedAction
	RolledBack []PlannedAction
	// FailOn is an action type that triggers a simulated error whenever encountered.
	FailOn schema.ActionType
	// FailAfter, if positive, causes the applier to succeed FailAfter times
	// then return a simulated error on all subsequent calls. Useful when the
	// plan has multiple actions of the same type and you need at least one
	// success before a failure.
	FailAfter  int
	applyCount int
}

// ApplyAction records the action. Returns a simulated error when FailOn
// matches the action type, or when FailAfter successes have been accumulated.
func (n *NoopApplier) ApplyAction(a PlannedAction) error {
	if n.FailOn != "" && a.Action.Type == n.FailOn {
		return fmt.Errorf("simulated failure on action %q target %q", a.Action.Type, a.Action.Target)
	}
	if n.FailAfter > 0 && n.applyCount >= n.FailAfter {
		return fmt.Errorf("simulated failure after %d applications (action %q target %q)",
			n.FailAfter, a.Action.Type, a.Action.Target)
	}
	n.applyCount++
	n.Applied = append(n.Applied, a)
	return nil
}

// RollbackAction records the rollback call.
func (n *NoopApplier) RollbackAction(a PlannedAction) error {
	n.RolledBack = append(n.RolledBack, a)
	return nil
}
