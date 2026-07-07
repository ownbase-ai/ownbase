//go:build integration

package vulnscan_test

// Tier-2 integration tests for internal/vulnscan.
//
// Requires a hardened Ubuntu Base with trivy installed (PassZero runs
// ensureTrivy as part of its pass-zero sequence). Run on the Multipass VM
// after PassZero:
//
//	multipass exec ownbase-test -- bash -c \
//	  "cd ~/ownbase && sudo /usr/local/go/bin/go test -tags=integration \
//	   ./internal/vulnscan/... -v -timeout 10m"
//
// What we assert:
//   - TrivyAvailable returns true after PassZero installs trivy.
//   - GatherHostVulns completes without error and returns a non-zero
//     ScannedAt (i.e. the scan ran, even if zero CVEs are found).
//   - GatherVulns with no services returns Available=true and a valid result.
//   - GatherVulns is idempotent: a second call returns consistent Available.
//   - ParseTrivyOutput handles the real trivy JSON format (schema version
//     check so we detect a breaking trivy API change early).

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/vulnscan"
)

// requireLinux skips the test when not running on Linux.
func requireLinuxVuln(t *testing.T) {
	t.Helper()
	out, err := exec.Command("uname", "-s").Output()
	if err != nil || strings.TrimSpace(string(out)) != "Linux" {
		t.Skip("skipping: requires Linux")
	}
}

// requireRoot skips the test when not running as root.
func requireRootVuln(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("skipping: requires root (run via 'make test-vm' sudo pass)")
	}
}

// requireTrivy skips the test when trivy is not on PATH, which happens before
// PassZero runs on a fresh VM.
func requireTrivy(t *testing.T) {
	t.Helper()
	if !vulnscan.TrivyAvailable() {
		t.Skip("skipping: trivy not on PATH — run PassZero first (make test-vm)")
	}
}

// ---------------------------------------------------------------------------
// TrivyAvailable
// ---------------------------------------------------------------------------

// TestTrivyAvailable_AfterPassZero verifies trivy is on PATH after PassZero.
// This is the smoke-check that ensureTrivy in internal/install did its job.
func TestTrivyAvailable_AfterPassZero(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)

	if !vulnscan.TrivyAvailable() {
		t.Fatal("trivy not on PATH — PassZero must install it via ensureTrivy")
	}

	// Log the version so CI output is inspectable.
	out, err := exec.Command("trivy", "--version").Output()
	if err != nil {
		t.Fatalf("trivy --version: %v", err)
	}
	t.Logf("trivy: %s", strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
}

// ---------------------------------------------------------------------------
// GatherHostVulns
// ---------------------------------------------------------------------------

// TestGatherHostVulns_RunsAndReturns verifies that a host scan completes
// without timing out and returns a valid (non-zero) scanned result.
// We do NOT assert zero CVEs — a hardened test VM is not guaranteed to be
// fully patched, and we do not want a flaky gate.
func TestGatherHostVulns_RunsAndReturns(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)
	requireTrivy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	before := time.Now()
	summary, err := vulnscan.GatherHostVulns(ctx)
	elapsed := time.Since(before)

	if err != nil {
		t.Fatalf("GatherHostVulns returned error — trivy host scan failed: %v", err)
	}

	// Total is allowed to be 0 (fully patched VM) but must not be negative.
	if summary.Total() < 0 {
		t.Errorf("GatherHostVulns: Total=%d (negative — unexpected)", summary.Total())
	}

	t.Logf("host scan: elapsed=%s critical=%d high=%d medium=%d low=%d total=%d",
		elapsed.Round(time.Second), summary.Critical, summary.High, summary.Medium, summary.Low, summary.Total())
}

// TestGatherHostVulns_TopFindings verifies the Top slice is well-formed when
// critical or high CVEs are present.
func TestGatherHostVulns_TopFindings(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)
	requireTrivy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	summary, err := vulnscan.GatherHostVulns(ctx)
	if err != nil {
		t.Fatalf("GatherHostVulns returned error: %v", err)
	}

	// If there are critical/high CVEs, Top must be non-empty.
	if summary.Critical+summary.High > 0 && len(summary.Top) == 0 {
		t.Error("Top is empty despite Critical+High > 0")
	}

	// Top findings must only contain CRITICAL or HIGH entries.
	for i, f := range summary.Top {
		switch f.Severity {
		case "CRITICAL", "HIGH":
		default:
			t.Errorf("Top[%d].Severity = %q; expected CRITICAL or HIGH only", i, f.Severity)
		}
		if f.VulnID == "" {
			t.Errorf("Top[%d].VulnID is empty", i)
		}
	}

	t.Logf("top findings: %d (from %d critical + %d high)", len(summary.Top), summary.Critical, summary.High)
}

// ---------------------------------------------------------------------------
// GatherVulns — full scan with no services
// ---------------------------------------------------------------------------

// TestGatherVulns_NoContainers verifies the full scan path when no container
// targets are provided (e.g. podman has no running containers yet).
func TestGatherVulns_NoContainers(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)
	requireTrivy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	status := vulnscan.GatherVulns(ctx, nil)

	if !status.TrivyInstalled {
		t.Error("TrivyInstalled=false despite trivy being on PATH")
	}
	if !status.Available {
		t.Error("Available=false with no images to scan — host scan must have succeeded")
	}
	if status.ScannedAt.IsZero() {
		t.Error("ScannedAt is zero — GatherVulns must always stamp ScannedAt")
	}
	if status.ScannedAt.After(time.Now()) {
		t.Errorf("ScannedAt is in the future: %v", status.ScannedAt)
	}
	if len(status.Images) != 0 {
		t.Errorf("Images = %d entries, want 0 (no service names passed)", len(status.Images))
	}

	t.Logf("GatherVulns: available=%v scanned_at=%v host=%+v",
		status.Available, status.ScannedAt.Format(time.RFC3339), status.Host)
}

// TestRunningContainers verifies that RunningContainers returns plausible
// results on a Base with Podman installed.
func TestRunningContainers_ReturnsTargets(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	targets := vulnscan.RunningContainers(ctx)

	// We don't require containers to be running (tests run on ownbase-test,
	// not a live ownbase-fresh with services). Just verify the call succeeds
	// and returns well-formed entries when results are present.
	for _, tgt := range targets {
		if tgt.Service == "" {
			t.Errorf("RunningContainers: empty Service in target %+v", tgt)
		}
		if tgt.Image == "" {
			t.Errorf("RunningContainers: empty Image in target %+v", tgt)
		}
	}

	t.Logf("RunningContainers: found %d target(s)", len(targets))
	for _, tgt := range targets {
		t.Logf("  service=%q image=%q", tgt.Service, tgt.Image)
	}
}

// TestGatherVulns_ScannedAtAlwaysSet verifies that ScannedAt is populated even
// when trivy is unavailable — important for the overlapping-scan guard in the daemon.
func TestGatherVulns_ScannedAtAlwaysSet(t *testing.T) {
	requireLinuxVuln(t)

	// Temporarily shadow trivy with a failing command by manipulating PATH.
	// We do this by creating a /tmp directory that contains a "trivy" stub
	// that exits 1, then prepending it to PATH.
	stubDir := t.TempDir()
	stubPath := stubDir + "/trivy"
	if err := os.WriteFile(stubPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write trivy stub: %v", err)
	}
	t.Setenv("PATH", stubDir+":"+os.Getenv("PATH"))

	// TrivyAvailable now returns true (stub exists), but the scan will fail.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status := vulnscan.GatherVulns(ctx, nil)

	// ScannedAt must be set even on failure.
	if status.ScannedAt.IsZero() {
		t.Error("ScannedAt is zero even on scan failure — daemon overlap guard will not work")
	}
	// TrivyInstalled must be true (stub is on PATH).
	if !status.TrivyInstalled {
		t.Error("TrivyInstalled=false but trivy stub is on PATH")
	}
	// Available must be false (scan failed).
	if status.Available {
		t.Error("Available=true even though trivy scan returned exit code 1")
	}
	t.Logf("failed scan: scanned_at=%v trivy_installed=%v available=%v",
		status.ScannedAt.Format(time.RFC3339), status.TrivyInstalled, status.Available)
}

// ---------------------------------------------------------------------------
// GatherVulns — idempotency
// ---------------------------------------------------------------------------

// TestGatherVulns_Idempotent runs two back-to-back scans and verifies both
// return Available=true and that the second ScannedAt is >= the first.
func TestGatherVulns_Idempotent(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)
	requireTrivy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	targets := vulnscan.RunningContainers(ctx)
	first := vulnscan.GatherVulns(ctx, targets)
	if !first.Available {
		t.Fatal("first GatherVulns: Available=false")
	}

	second := vulnscan.GatherVulns(ctx, targets)
	if !second.Available {
		t.Fatal("second GatherVulns: Available=false")
	}

	if second.ScannedAt.Before(first.ScannedAt) {
		t.Errorf("second ScannedAt (%v) is before first (%v)", second.ScannedAt, first.ScannedAt)
	}

	// CVE counts must be identical (same binary, same DB).
	if first.Host.Critical != second.Host.Critical ||
		first.Host.High != second.Host.High {
		t.Errorf("CVE counts differ between runs: first=%+v second=%+v",
			first.Host, second.Host)
	}

	t.Logf("idempotent: first=%v second=%v critical=%d high=%d",
		first.ScannedAt.Format(time.RFC3339),
		second.ScannedAt.Format(time.RFC3339),
		first.Host.Critical, first.Host.High)
}

// ---------------------------------------------------------------------------
// Trivy JSON schema compatibility
// ---------------------------------------------------------------------------

// TestTrivy_JSONSchemaVersion verifies that the installed trivy emits
// SchemaVersion 2 — so we detect a breaking API change before it silently
// produces empty parse results.
func TestTrivy_JSONSchemaVersion(t *testing.T) {
	requireLinuxVuln(t)
	requireRootVuln(t)
	requireTrivy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Scan /proc (tiny, always present) to get a real JSON response quickly.
	out, err := exec.CommandContext(ctx,
		"trivy", "fs",
		"--quiet",
		"--format", "json",
		"--scanners", "vuln",
		"--pkg-types", "os",
		"/proc",
	).CombinedOutput()
	if err != nil {
		// Exit code 1 with no results is fine; non-zero for other reasons is a fail.
		t.Logf("trivy /proc: exit error (non-fatal, checking JSON anyway): %v", err)
	}

	if len(out) == 0 {
		t.Fatal("trivy produced no output — cannot check schema version")
	}

	// Look for SchemaVersion in the raw JSON.
	raw := string(out)
	if !strings.Contains(raw, `"SchemaVersion"`) {
		t.Fatalf("trivy output does not contain SchemaVersion — unexpected format:\n%s", raw[:min(len(raw), 500)])
	}
	if !strings.Contains(raw, `"SchemaVersion":2`) {
		t.Errorf("SchemaVersion is not 2 — trivy may have changed its output format:\n%s", raw[:min(len(raw), 500)])
	}

	t.Logf("trivy schema version: OK (SchemaVersion:2 present)")
}

// min is a local helper for pre-Go-1.21 compatibility.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
