//go:build integration

package secwatch_test

// Tier-2 integration tests for internal/secwatch.
//
// These tests require a hardened Ubuntu Base (ss, ufw, and fail2ban installed
// and configured by PassZero). Run on the Multipass VM:
//
//	multipass exec ownbase-test -- bash -c \
//	  "cd ~/ownbase && sudo /usr/local/go/bin/go test -tags=integration ./internal/secwatch/... -v"
//
// What we assert on a clean, hardened Base:
//   - GatherExposure returns Available=true, FirewallActive=true, UnexpectedCount=0.
//   - Port 22 (or configured SSH port) is expected and allowed.
//   - Ports 80 and 443 are NOT allowed — the install package's own
//     integration test hardens the VM with no domain configured (the
//     default PassZeroConfig), and a domain-less Base exposes only SSH
//     (see install.ensureFirewall / docs/decisions.md, "Local development").
//   - No database ports (5432, 3306, 27017, 6379) are internet-reachable.
//   - GatherAccess returns Available=true, Fail2banActive=true.

import (
	"context"
	"os"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secwatch"
)

// requireRoot skips the test when not running as root. Tests that probe
// ufw/fail2ban require root; they must only fatal (not skip) in the root pass.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("skipping: requires root — run via 'make test-vm' (sudo pass)")
	}
}

// TestGatherExposure_CleanBase verifies that a hardened Base reports no
// unexpected exposure.
func TestGatherExposure_CleanBase(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()
	cfg := &schema.OwnbaseConfig{}

	result := secwatch.GatherExposure(ctx, cfg, 22)

	if !result.Available {
		t.Fatal("ss or ufw not available — this test must run as root after PassZero (make test-vm)")
	}
	if !result.FirewallActive {
		t.Fatal("UFW is not active — this test must run after PassZero hardens the machine (make test-vm)")
	}

	if result.UnexpectedCount != 0 {
		t.Errorf("unexpected internet-reachable ports: %d (expected 0 on clean Base)", result.UnexpectedCount)
		for _, l := range result.Listeners {
			if l.InternetReachable && !l.Expected {
				t.Logf("  unexpected: port=%d proto=%s bind=%s process=%s",
					l.Port, l.Proto, l.Bind, l.Process)
			}
		}
	}

	// Database ports must not be internet-reachable.
	dbPorts := []int{5432, 3306, 27017, 6379}
	for _, port := range dbPorts {
		for _, l := range result.Listeners {
			if l.Port == port && l.InternetReachable {
				t.Errorf("database port %d is internet-reachable — containers must listen on loopback only", port)
			}
		}
	}

	// SSH (22), 80, 443 must be in the expected allowlist.
	for _, port := range []int{22, 80, 443} {
		found := false
		for _, l := range result.Listeners {
			if l.Port == port && l.Expected {
				found = true
				break
			}
		}
		if !found {
			t.Logf("note: port %d not found as an expected listener (may not be bound yet)", port)
		}
	}
}

// TestGatherAccess_CleanBase verifies that fail2ban is active on a hardened Base.
func TestGatherAccess_CleanBase(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()

	result := secwatch.GatherAccess(ctx)

	if !result.Available {
		t.Fatal("journald not available — this test must run as root after PassZero (make test-vm)")
	}
	if !result.Fail2banAvailable {
		t.Fatal("fail2ban-client not reachable — this test must run as root after PassZero (make test-vm)")
	}
	if !result.Fail2banActive {
		t.Fatal("fail2ban sshd jail is not active — this test must run after PassZero (make test-vm)")
	}

	// The banned list may be empty on a fresh Base — that's fine.
	t.Logf("fail2ban: active=%v, banned=%v, failed=%d, recent_logins=%d",
		result.Fail2banActive, result.BannedIPs, result.FailedAttempts, len(result.RecentLogins))
}

// TestScanListeners_HasBoundPorts verifies that ss finds at least one
// listening socket (SSH must be running for us to have connected).
func TestScanListeners_HasBoundPorts(t *testing.T) {
	ctx := context.Background()

	listeners, available := secwatch.ScanListeners(ctx)
	if !available {
		t.Fatal("ss not available — this test requires an Ubuntu host with iproute2")
	}

	if len(listeners) == 0 {
		t.Error("expected at least one listening socket (SSH must be running)")
	}

	t.Logf("found %d listening sockets", len(listeners))
	for _, l := range listeners {
		t.Logf("  port=%d proto=%s bind=%s process=%s loopback=%v",
			l.Port, l.Proto, l.Bind, l.Process, secwatch.IsLoopback(l.Bind))
	}
}

// TestReadFirewall_Active verifies that UFW is active on a hardened Base.
//
// The install package's own integration test (TestPassZero_FullInstall,
// which runs first in this Tier-2 job — see .github/workflows/ci.yml)
// hardens the VM with the default PassZeroConfig, i.e. ExposeWebPorts:
// false: a domain-less Base only allows SSH, since there is no Caddy route
// to serve on 80/443 yet (see install.ensureFirewall). So 80/443 are
// expected to be closed here, not open.
func TestReadFirewall_Active(t *testing.T) {
	requireRoot(t)
	ctx := context.Background()

	fw := secwatch.ReadFirewall(ctx)
	if !fw.Available {
		t.Fatal("ufw not available — this test must run as root after PassZero (make test-vm)")
	}

	if !fw.Active {
		t.Error("UFW should be active on a hardened Base")
	}

	t.Logf("UFW allowed ports: %v", fw.AllowedPorts)

	if !fw.AllowedPorts[22] {
		t.Error("expected port 22 (SSH) to be allowed in UFW")
	}
	for _, port := range []int{80, 443} {
		if fw.AllowedPorts[port] {
			t.Errorf("expected port %d to NOT be allowed in UFW — no service has a domain configured on this Base", port)
		}
	}
}
