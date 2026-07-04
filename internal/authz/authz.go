// Package authz implements the authorization checkpoint and audit log.
// The V1 checkpoint is trivially permissive (all-autonomous); post-V1 it
// evaluates the customer's governance policy. M3 wires the real audit log;
// this file establishes the first-class seam from M0.5 forward.
//
// Architecture Principle 15: the checkpoint is a first-class component, not
// an afterthought grafted on later. Every Agent action routes through it even
// in V1 when it always authorizes.
package authz

import (
	"fmt"

	"github.com/ownbase/ownbase/internal/schema"
)

// Checkpoint is the authorization gate all Agent actions pass through before
// executing. The V1 implementation is trivially permissive; the interface is
// designed so post-V1 policy evaluation is a drop-in replacement.
type Checkpoint interface {
	// Authorize returns nil if the action is permitted, or an error if it is
	// refused. An action that is not in the taxonomy must always be refused,
	// regardless of policy.
	Authorize(action schema.Action) error
}

// TrivialCheckpoint is the V1 all-autonomous, owner-only checkpoint. It
// approves every action in the taxonomy and refuses any action not in it.
// The taxonomy check is the only enforcement that matters in V1; the tier is
// recorded in the audit log but never blocks.
type TrivialCheckpoint struct{}

// NewTrivialCheckpoint returns the V1 checkpoint. The returned value is safe
// for concurrent use.
func NewTrivialCheckpoint() Checkpoint {
	return TrivialCheckpoint{}
}

// Authorize approves every taxonomied action. Actions with an empty Type are
// refused because they cannot have passed through schema.NewAction.
func (TrivialCheckpoint) Authorize(action schema.Action) error {
	if action.Type == "" {
		return fmt.Errorf("authz: action has empty type (was it created via schema.NewAction?)")
	}
	// V1: all-autonomous policy — every taxonomy-validated action is authorized.
	return nil
}
