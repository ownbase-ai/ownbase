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
