package schema

import "fmt"

// RiskTier classifies how much autonomy the Agent has before executing an
// action. The three tiers are defined in Architecture Principle 15 and the
// Identity & Authority foundation doc.
type RiskTier string

const (
	// TierAutonomous — the Agent acts immediately, no notification.
	TierAutonomous RiskTier = "autonomous"
	// TierNotify — the Agent acts and simultaneously notifies the owner.
	TierNotify RiskTier = "notify"
	// TierApprove — the Agent suspends until the Authority device approves.
	// Post-V1: same code path, real device checkpoint.
	TierApprove RiskTier = "approve"
)

// ActionType is a closed enumeration of every action the OwnBase Daemon may
// take. An action that is not in this taxonomy cannot be executed — that
// property is enforced by NewAction. The taxonomy exists in V1 even though V1
// runs with a trivially permissive (all-autonomous) policy; post-V1 policy
// declarations reference it.
type ActionType string

// The taxonomy. Every entry maps to a default risk tier. The comment on each
// line is the canonical tier assignment; the defaultTiers map below is the
// machine-readable version of the same information.
const (
	// Reconcile actions
	ActionServiceStart   ActionType = "service.start"   // autonomous
	ActionServiceStop    ActionType = "service.stop"    // notify
	ActionServiceRestart ActionType = "service.restart" // autonomous
	ActionServiceReload  ActionType = "service.reload"  // autonomous

	// Deploy actions
	ActionDeployApply    ActionType = "deploy.apply"    // notify
	ActionDeployRollback ActionType = "deploy.rollback" // notify

	// Secret actions
	ActionSecretIssue  ActionType = "secret.issue"  // autonomous
	ActionSecretRotate ActionType = "secret.rotate" // notify

	// Backup actions
	ActionBackupRun     ActionType = "backup.run"     // autonomous
	ActionRestoreVerify ActionType = "restore.verify" // autonomous
	ActionRestoreApply  ActionType = "restore.apply"  // approve

	// Update actions
	// ActionUpdatePinRef is emitted when the agent resolves a blank ref: and
	// commits the default-branch HEAD SHA back to ownbase.yaml.
	ActionUpdatePinRef ActionType = "update.pin_ref" // autonomous

	// Host / security actions
	ActionHostHarden            ActionType = "host.harden"          // autonomous
	ActionHostInstallRuntime    ActionType = "host.install_runtime" // autonomous
	ActionHostConfigureFirewall ActionType = "host.firewall"        // autonomous
	ActionHostAutoUpdates       ActionType = "host.auto_updates"    // autonomous
	ActionHostFail2ban          ActionType = "host.fail2ban"        // autonomous
	ActionPortClose             ActionType = "port.close"           // notify
	// ActionPortExposed is emitted on the transition into or out of unexpected
	// internet-reachable exposure. Recorded by the secwatch probe in the agent.
	ActionPortExposed ActionType = "port.exposed" // notify
	ActionCertRenew   ActionType = "cert.renew"   // autonomous

	// Build actions
	ActionBuildImage ActionType = "build.image" // autonomous

	// Git host actions
	ActionGitRepoInit    ActionType = "git.repo_init"    // autonomous
	ActionGitHookInstall ActionType = "git.hook_install" // autonomous
)

// defaultTiers is the canonical tier for each action type. This is the
// machine-readable version of the taxonomy comment above.
var defaultTiers = map[ActionType]RiskTier{
	ActionServiceStart:   TierAutonomous,
	ActionServiceStop:    TierNotify,
	ActionServiceRestart: TierAutonomous,
	ActionServiceReload:  TierAutonomous,

	ActionDeployApply:    TierNotify,
	ActionDeployRollback: TierNotify,

	ActionSecretIssue:  TierAutonomous,
	ActionSecretRotate: TierNotify,

	ActionBackupRun:     TierAutonomous,
	ActionRestoreVerify: TierAutonomous,
	ActionRestoreApply:  TierApprove,

	ActionUpdatePinRef: TierAutonomous,

	ActionHostHarden:            TierAutonomous,
	ActionHostInstallRuntime:    TierAutonomous,
	ActionHostConfigureFirewall: TierAutonomous,
	ActionHostAutoUpdates:       TierAutonomous,
	ActionHostFail2ban:          TierAutonomous,
	ActionPortClose:             TierNotify,
	ActionPortExposed:           TierNotify,
	ActionCertRenew:             TierAutonomous,

	ActionBuildImage: TierAutonomous,

	ActionGitRepoInit:    TierAutonomous,
	ActionGitHookInstall: TierAutonomous,
}

// Action is a taxonomy-checked, tier-carrying unit of Agent intent. The only
// constructor is NewAction, which refuses any type not in the taxonomy.
type Action struct {
	Type        ActionType
	DefaultTier RiskTier
	// Target is a human-readable description of what the action acts on
	// (e.g. service name, file path).
	Target string
}

// NewAction constructs an Action, returning an error if actionType is not in
// the taxonomy. This is the enforcement point: an action that does not appear
// in the taxonomy cannot be executed.
func NewAction(actionType ActionType, target string) (Action, error) {
	tier, ok := defaultTiers[actionType]
	if !ok {
		return Action{}, fmt.Errorf("unknown action type %q: not in taxonomy", actionType)
	}
	return Action{
		Type:        actionType,
		DefaultTier: tier,
		Target:      target,
	}, nil
}

// MustNewAction is NewAction that panics on an unknown type. Use only in
// tests or package-level init where a compile-time constant is passed.
func MustNewAction(actionType ActionType, target string) Action {
	a, err := NewAction(actionType, target)
	if err != nil {
		panic(err)
	}
	return a
}

// AllActions returns a snapshot of the taxonomy as a slice of (ActionType,
// RiskTier) pairs, sorted by action type string. Useful for documentation
// generation and exhaustiveness tests.
func AllActions() []Action {
	out := make([]Action, 0, len(defaultTiers))
	for at, tier := range defaultTiers {
		out = append(out, Action{Type: at, DefaultTier: tier})
	}
	// Stable sort so the output is deterministic.
	sortActions(out)
	return out
}

func sortActions(actions []Action) {
	// Insertion sort — the slice is tiny (< 30 entries) and we have no
	// dependency on sort.Slice here to keep the package import-free.
	for i := 1; i < len(actions); i++ {
		for j := i; j > 0 && actions[j].Type < actions[j-1].Type; j-- {
			actions[j], actions[j-1] = actions[j-1], actions[j]
		}
	}
}
