package main

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// isCheckoutMissingError (Fix 5)
// ---------------------------------------------------------------------------

func TestIsCheckoutMissingError_NotExist(t *testing.T) {
	err := errors.New("parse /opt/ownbase/checkout/ownbase.yaml: no such file or directory")
	if !isCheckoutMissingError(err) {
		t.Error("expected isCheckoutMissingError to return true for 'no such file' error")
	}
}

func TestIsCheckoutMissingError_NotExistVariant(t *testing.T) {
	err := errors.New("parse ownbase.yaml: file does not exist")
	if !isCheckoutMissingError(err) {
		t.Error("expected isCheckoutMissingError to return true for 'not exist' error")
	}
}

func TestIsCheckoutMissingError_Nil(t *testing.T) {
	if isCheckoutMissingError(nil) {
		t.Error("expected isCheckoutMissingError(nil) = false")
	}
}

func TestIsCheckoutMissingError_OtherError(t *testing.T) {
	err := errors.New("parse ownbase.yaml: yaml: unmarshal error")
	if isCheckoutMissingError(err) {
		t.Error("expected isCheckoutMissingError to return false for parse/unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// isConfigError (Fix 6)
// ---------------------------------------------------------------------------

func TestIsConfigError_ParseError(t *testing.T) {
	err := errors.New("parse ownbase.yaml: yaml: line 5: could not find expected ':'")
	if !isConfigError(err) {
		t.Error("expected isConfigError to return true for parse error")
	}
}

func TestIsConfigError_Nil(t *testing.T) {
	if isConfigError(nil) {
		t.Error("expected isConfigError(nil) = false")
	}
}

func TestIsConfigError_TransientError(t *testing.T) {
	err := errors.New("diff: query podman: context deadline exceeded")
	if isConfigError(err) {
		t.Error("expected isConfigError to return false for transient error")
	}
}

// ---------------------------------------------------------------------------
// startupCaddyReload (forced post-reboot Caddy reload, consumed only on success)
// ---------------------------------------------------------------------------

func TestStartupCaddyReload_PeekDoesNotConsume(t *testing.T) {
	// Reset guard so the test is deterministic regardless of prior calls.
	startupCaddyReloadMu.Lock()
	startupCaddyReloadDone = false
	startupCaddyReloadMu.Unlock()

	// Peeking must stay pending until explicitly marked done — this is what
	// lets a failed reconcile retry the forced reload instead of skipping it.
	if !startupCaddyReloadPending() {
		t.Fatal("expected pending=true before any success")
	}
	if !startupCaddyReloadPending() {
		t.Fatal("expected pending=true on repeated peek (peek must not consume)")
	}

	markStartupCaddyReloadDone()

	if startupCaddyReloadPending() {
		t.Fatal("expected pending=false after markStartupCaddyReloadDone()")
	}
	// Idempotent.
	markStartupCaddyReloadDone()
	if startupCaddyReloadPending() {
		t.Fatal("expected pending=false to remain after repeated mark")
	}
}
