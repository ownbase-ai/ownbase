package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// ---------------------------------------------------------------------------
// annotateSecretsFingerprints (restart-on-secrets-change)
// ---------------------------------------------------------------------------

func TestAnnotateSecretsFingerprints(t *testing.T) {
	dir := t.TempDir()
	// "api" has a secrets file; "web" does not.
	if err := os.WriteFile(filepath.Join(dir, "api.yaml.age"), []byte("ciphertext-v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	units := map[string]string{
		"ownbase-api.container": "[Container]\nImage=x\n",
		"ownbase-web.container": "[Container]\nImage=y\n",
		"ownbase-net.network":   "[Network]\n",
	}
	annotateSecretsFingerprints(units, dir)

	if !strings.Contains(units["ownbase-api.container"], secretsFingerprintPrefix) {
		t.Errorf("api unit missing secrets fingerprint:\n%s", units["ownbase-api.container"])
	}
	if strings.Contains(units["ownbase-web.container"], secretsFingerprintPrefix) {
		t.Error("web unit must not carry a fingerprint (no secrets file)")
	}
	if strings.Contains(units["ownbase-net.network"], secretsFingerprintPrefix) {
		t.Error("non-container unit must not be annotated")
	}

	first := units["ownbase-api.container"]

	// Stable when the secrets file is unchanged.
	units2 := map[string]string{"ownbase-api.container": "[Container]\nImage=x\n"}
	annotateSecretsFingerprints(units2, dir)
	if units2["ownbase-api.container"] != first {
		t.Error("fingerprint must be stable for an unchanged secrets file")
	}

	// Changes when the secrets file changes — this is what triggers a restart.
	if err := os.WriteFile(filepath.Join(dir, "api.yaml.age"), []byte("ciphertext-v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	units3 := map[string]string{"ownbase-api.container": "[Container]\nImage=x\n"}
	annotateSecretsFingerprints(units3, dir)
	if units3["ownbase-api.container"] == first {
		t.Error("fingerprint must change after the secrets file changes")
	}
}
