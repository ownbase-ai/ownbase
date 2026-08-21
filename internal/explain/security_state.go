package explain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultSecurityStatePath is where the daemon persists security-loop state
// that must survive a reboot (last_patch_at, last_core_rebuild_at) or trigger
// work on the next boot (rescan_on_boot). Under /opt/ownbase/state, not
// runtime/ — the compiler is the single writer of runtime/.
const DefaultSecurityStatePath = "/opt/ownbase/state/security.json"

// SecurityState is durable daemon state for the host-patching and core-rebuild
// loops.
type SecurityState struct {
	// LastPatchAt is when /security/fix last finished successfully.
	// Used by checkup to suppress "Apply patches" while counts are still
	// pre-patch (scanned_at < last_patch_at).
	LastPatchAt time.Time `json:"last_patch_at,omitempty"`
	// LastCoreRebuildAt is when POST /upgrade last finished successfully.
	// Used by checkup to suppress "Rebuild Caddy" while counts are still
	// pre-rebuild, and to detect a proven-ineffective rebuild (newer scan
	// still shows fixable core CVEs).
	LastCoreRebuildAt time.Time `json:"last_core_rebuild_at,omitempty"`
	// RescanOnBoot, when true, tells the next daemon start to run a CVE scan
	// immediately instead of waiting the normal 5-minute startup delay.
	// Set by /security/reboot; cleared after the scan is triggered.
	RescanOnBoot bool `json:"rescan_on_boot,omitempty"`
}

// LoadSecurityState reads path. Missing or unreadable files yield a zero
// value (not an error) — a fresh Base has never patched.
func LoadSecurityState(path string) SecurityState {
	data, err := os.ReadFile(path)
	if err != nil {
		return SecurityState{}
	}
	var st SecurityState
	if err := json.Unmarshal(data, &st); err != nil {
		return SecurityState{}
	}
	return st
}

// SaveSecurityState writes st atomically (temp + rename).
func SaveSecurityState(path string, st SecurityState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir security state: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write security state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename security state: %w", err)
	}
	return nil
}

// MarkPatched sets LastPatchAt to now and persists.
func MarkPatched(path string) error {
	st := LoadSecurityState(path)
	st.LastPatchAt = time.Now().UTC()
	return SaveSecurityState(path, st)
}

// MarkCoreRebuilt sets LastCoreRebuildAt to now and persists.
func MarkCoreRebuilt(path string) error {
	st := LoadSecurityState(path)
	st.LastCoreRebuildAt = time.Now().UTC()
	return SaveSecurityState(path, st)
}

// MarkRescanOnBoot sets RescanOnBoot and persists.
func MarkRescanOnBoot(path string) error {
	st := LoadSecurityState(path)
	st.RescanOnBoot = true
	return SaveSecurityState(path, st)
}

// ClearRescanOnBoot clears the flag and persists. No-op when already clear.
func ClearRescanOnBoot(path string) error {
	st := LoadSecurityState(path)
	if !st.RescanOnBoot {
		return nil
	}
	st.RescanOnBoot = false
	return SaveSecurityState(path, st)
}
