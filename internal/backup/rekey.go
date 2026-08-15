package backup

// rekey.go rotates the restic repository encryption password in two crash-safe
// phases. Restic supports multiple keys: both passwords open the repo until
// the final remove, so any intermediate state is recoverable by re-running.
//
//	add      — ensure newPassword is a key (no-op if it already opens)
//	finalize — caller has written newPassword to the Base secret + vault;
//	           verify it opens, then remove every other key
//
// The Base secret update is owned by the daemon API (secrets merge), not this
// package — finalize only touches the restic keyring.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

// RekeyPhase names one step of the password rotation.
type RekeyPhase string

const (
	// RekeyPhaseAdd adds the new password as a second key.
	RekeyPhaseAdd RekeyPhase = "add"
	// RekeyPhaseFinalize removes every key except the one for newPassword.
	// Requires that the Base secret (and typically the vault escrow) already
	// hold newPassword so a crash mid-remove still leaves a working secret.
	RekeyPhaseFinalize RekeyPhase = "finalize"
)

// CredFingerprint returns an 8-hex-char fingerprint of the restic password
// for dual-write drift checks. Empty password → empty fingerprint. The
// prefix binds the hash to this purpose so a leaked fingerprint is not a
// generic password hash.
func CredFingerprint(password string) string {
	if password == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("ownbase-restic-cred-v1:" + password))
	return hex.EncodeToString(sum[:])[:8]
}

// RekeyResult is the outcome of one rekey phase.
type RekeyResult struct {
	Phase       RekeyPhase `json:"phase"`
	AlreadyDone bool       `json:"already_done,omitempty"`
	KeysRemoved int        `json:"keys_removed,omitempty"`
	Fingerprint string     `json:"fingerprint,omitempty"`
}

// Rekey runs one phase of password rotation. newPassword must be non-empty.
// cfg.Credentials must contain the password that currently opens the repo
// for the add phase (the Base secret); for finalize it should already be
// the new password (daemon swaps the secret before calling finalize).
func Rekey(ctx context.Context, cfg Config, phase RekeyPhase, newPassword string) (RekeyResult, error) {
	c := cfg.withDefaults()
	if err := c.Validate(); err != nil {
		return RekeyResult{}, err
	}
	if newPassword == "" {
		return RekeyResult{}, fmt.Errorf("backup rekey: new password is required")
	}

	switch phase {
	case RekeyPhaseAdd:
		return rekeyAdd(ctx, c, newPassword)
	case RekeyPhaseFinalize:
		return rekeyFinalize(ctx, c, newPassword)
	default:
		return RekeyResult{}, fmt.Errorf("backup rekey: unknown phase %q (want add or finalize)", phase)
	}
}

func rekeyAdd(ctx context.Context, cfg Config, newPassword string) (RekeyResult, error) {
	if passwordOpens(ctx, cfg, newPassword) {
		return RekeyResult{
			Phase:       RekeyPhaseAdd,
			AlreadyDone: true,
			Fingerprint: CredFingerprint(newPassword),
		}, nil
	}
	if err := keyAdd(ctx, cfg, newPassword); err != nil {
		return RekeyResult{}, err
	}
	if !passwordOpens(ctx, cfg, newPassword) {
		return RekeyResult{}, fmt.Errorf("backup rekey add: new password does not open the repository after key add")
	}
	return RekeyResult{
		Phase:       RekeyPhaseAdd,
		Fingerprint: CredFingerprint(newPassword),
	}, nil
}

func rekeyFinalize(ctx context.Context, cfg Config, newPassword string) (RekeyResult, error) {
	// Finalize always authenticates with the new password — the daemon must
	// have swapped the Base secret before calling this phase.
	cfg.PasswordFile = ""
	cfg.Credentials = copyCreds(cfg.Credentials)
	cfg.Credentials["RESTIC_PASSWORD"] = newPassword

	if !passwordOpens(ctx, cfg, newPassword) {
		return RekeyResult{}, fmt.Errorf("backup rekey finalize: new password does not open the repository — run phase add first and ensure the Base secret was updated")
	}

	if cfg.DryRun {
		return RekeyResult{
			Phase:       RekeyPhaseFinalize,
			AlreadyDone: true,
			Fingerprint: CredFingerprint(newPassword),
		}, nil
	}

	keys, err := keyList(ctx, cfg)
	if err != nil {
		return RekeyResult{}, err
	}

	// Identify the key that matches the current session (the new password)
	// and remove every other one.
	var currentID string
	for _, k := range keys {
		if k.Current {
			currentID = k.ID
			break
		}
	}
	if currentID == "" && len(keys) == 1 {
		currentID = keys[0].ID
	}
	if currentID == "" {
		return RekeyResult{}, fmt.Errorf("backup rekey finalize: could not identify the current key among %d keys", len(keys))
	}

	removed := 0
	for _, k := range keys {
		if k.ID == currentID {
			continue
		}
		if err := keyRemove(ctx, cfg, k.ID); err != nil {
			return RekeyResult{}, fmt.Errorf("backup rekey finalize: remove key %s: %w (new password still works; re-run finalize)", k.ID, err)
		}
		removed++
	}

	if cfg.AuditLog != nil {
		action, _ := schema.NewAction(schema.ActionSecretRotate, "backup")
		_ = cfg.AuditLog.Record(action, authz.OutcomeApplied, fmt.Sprintf("restic rekey finalize removed %d key(s)", removed))
	}

	return RekeyResult{
		Phase:       RekeyPhaseFinalize,
		AlreadyDone: removed == 0 && len(keys) == 1,
		KeysRemoved: removed,
		Fingerprint: CredFingerprint(newPassword),
	}, nil
}
