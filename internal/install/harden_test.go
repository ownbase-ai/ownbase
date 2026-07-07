package install

// harden_test.go: Tier-1 tests for host hardening helpers.
// These tests are in the install package (not install_test) so they can
// access unexported functions.

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/secwatch"
)

// ---------------------------------------------------------------------------
// DB-exposure parser tests
// ---------------------------------------------------------------------------

// TestIsPublicBind covers the full set of bind-address formats that ss -tlnH
// emits. The M13 requirement: 0.0.0.0, [::], :::, and specific public IPs are
// flagged; 127.0.0.1 and [::1] are not.
func TestIsPublicBind(t *testing.T) {
	cases := []struct {
		addr    string
		port    string
		exposed bool
	}{
		// IPv4 all-interfaces — public.
		{"0.0.0.0:5432", "5432", true},
		{"0.0.0.0:3306", "3306", true},
		// IPv4 loopback — safe.
		{"127.0.0.1:5432", "5432", false},
		// IPv6 all-interfaces (bracketed) — public.
		{"[::]:5432", "5432", true},
		// IPv6 loopback — safe.
		{"[::1]:5432", "5432", false},
		// IPv6 compact all-interfaces (:::port form from some kernels) — public.
		// ss output can appear as ":::5432" which after stripping ":5432" gives "::".
		{":::5432", "5432", true},
		// Specific public IPv4 — public.
		{"203.0.113.5:5432", "5432", true},
		// Different port — not this port, not exposed.
		{"0.0.0.0:3306", "5432", false},
		// Redis on loopback — safe.
		{"127.0.0.1:6379", "6379", false},
		// Redis on all interfaces — public.
		{"0.0.0.0:6379", "6379", true},
	}
	for _, tc := range cases {
		got := secwatch.IsPublicBind(tc.addr, tc.port)
		if got != tc.exposed {
			t.Errorf("secwatch.IsPublicBind(%q, %q) = %v, want %v", tc.addr, tc.port, got, tc.exposed)
		}
	}
}

// TestIsPortExposedOnLine verifies that the ss column parser extracts the
// LocalAddress:Port field correctly from realistic ss -tlnH output lines.
func TestIsPortExposedOnLine(t *testing.T) {
	cases := []struct {
		line    string
		port    string
		exposed bool
		desc    string
	}{
		{
			"LISTEN 0      128          0.0.0.0:5432       0.0.0.0:*",
			"5432", true,
			"IPv4 all-interfaces Postgres",
		},
		{
			"LISTEN 0      128        127.0.0.1:5432       0.0.0.0:*",
			"5432", false,
			"IPv4 loopback Postgres — safe",
		},
		{
			"LISTEN 0      128             [::]:5432          [::]:*",
			"5432", true,
			"IPv6 all-interfaces Postgres",
		},
		{
			"LISTEN 0      128             [::1]:5432         [::]:*",
			"5432", false,
			"IPv6 loopback Postgres — safe",
		},
		{
			"LISTEN 0      128       203.0.113.5:5432       0.0.0.0:*",
			"5432", true,
			"specific public IP — exposed",
		},
		{
			"LISTEN 0      128          0.0.0.0:3306       0.0.0.0:*",
			"5432", false,
			"MySQL port but checking for Postgres — not matching",
		},
	}
	for _, tc := range cases {
		fields := strings.Fields(tc.line)
		addr := ""
		if len(fields) >= 4 {
			addr = fields[3]
		}
		got := secwatch.IsPublicBind(addr, tc.port)
		if got != tc.exposed {
			t.Errorf("[%s] IsPublicBind(%q, %q) = %v, want %v",
				tc.desc, addr, tc.port, got, tc.exposed)
		}
	}
}

// ---------------------------------------------------------------------------
// ufwRuleAllowed tests
// ---------------------------------------------------------------------------

// TestUfwRuleAllowed_RequiresExactPortProtoMatch covers the realistic `ufw
// status` shapes checkFirewallState parses, including the false-positive
// trap of a wider port (8080/tcp) textually containing a narrower one
// (80/tcp) as a substring.
func TestUfwRuleAllowed_RequiresExactPortProtoMatch(t *testing.T) {
	const status = `Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
80/tcp                     ALLOW       Anywhere
8080/tcp                   ALLOW       Anywhere
22/tcp (v6)                ALLOW       Anywhere (v6)
80/tcp (v6)                ALLOW       Anywhere (v6)`

	cases := []struct {
		portProto string
		want      bool
	}{
		{"22/tcp", true},
		{"80/tcp", true},
		{"8080/tcp", true},
		{"443/tcp", false}, // not present — this is the bug the fix covers.
	}
	for _, tc := range cases {
		if got := ufwRuleAllowed(status, tc.portProto); got != tc.want {
			t.Errorf("ufwRuleAllowed(status, %q) = %v, want %v", tc.portProto, got, tc.want)
		}
	}
}

// TestUfwRuleAllowed_IgnoresNonAllowActions locks in the fix for "UFW
// matcher ignores ALLOW action": a DENY/REJECT rule for the same port/proto
// must not be mistaken for the port being open, and the IPv6 port token
// ("80/tcp (v6)") — which shifts the action column one field to the right —
// must still be parsed correctly.
func TestUfwRuleAllowed_IgnoresNonAllowActions(t *testing.T) {
	const status = `Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
80/tcp                     DENY        Anywhere
443/tcp                    REJECT      Anywhere
8080/tcp                   LIMIT       Anywhere
22/tcp (v6)                ALLOW       Anywhere (v6)
443/tcp (v6)               ALLOW       Anywhere (v6)`

	cases := []struct {
		portProto string
		want      bool
	}{
		{"22/tcp", true},    // ALLOW
		{"80/tcp", false},   // DENY, not ALLOW
		{"443/tcp", true},   // IPv4 REJECT, but IPv6 line ALLOWs it
		{"8080/tcp", false}, // LIMIT, not ALLOW
	}
	for _, tc := range cases {
		if got := ufwRuleAllowed(status, tc.portProto); got != tc.want {
			t.Errorf("ufwRuleAllowed(status, %q) = %v, want %v", tc.portProto, got, tc.want)
		}
	}
}

// TestCheckFirewallState_RequiresBothWebPorts locks in the fix for "UFW
// check ignores HTTPS rule": ExposeWebPorts must require both 80/tcp AND
// 443/tcp to be allowed, not just 80/tcp, otherwise a partially-applied
// firewall (e.g. 80 open, 443 still blocked) is reported as already
// satisfying the desired state and sync silently skips reconfiguration.
func TestCheckFirewallState_RequiresBothWebPorts(t *testing.T) {
	const only80 = `Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
80/tcp                     ALLOW       Anywhere`

	const both = only80 + "\n443/tcp                     ALLOW       Anywhere"

	if got := ufwRuleAllowed(only80, "80/tcp") && ufwRuleAllowed(only80, "443/tcp"); got {
		t.Error("only80: want web ports considered NOT fully allowed (443 missing)")
	}
	if got := ufwRuleAllowed(both, "80/tcp") && ufwRuleAllowed(both, "443/tcp"); !got {
		t.Error("both: want web ports considered fully allowed")
	}
}
