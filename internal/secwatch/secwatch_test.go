package secwatch_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secwatch"
)

// ---------------------------------------------------------------------------
// IsLoopback
// ---------------------------------------------------------------------------

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		bind string
		want bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"0.0.0.0", false},
		{"::", false},
		{"203.0.113.5", false},
		{"10.88.0.1", false},
	}
	for _, tc := range cases {
		got := secwatch.IsLoopback(tc.bind)
		if got != tc.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", tc.bind, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// IsPublicBind (delegates from harden.go via secwatch)
// ---------------------------------------------------------------------------

func TestIsPublicBind(t *testing.T) {
	cases := []struct {
		addrPort string
		port     string
		exposed  bool
	}{
		{"0.0.0.0:5432", "5432", true},
		{"0.0.0.0:3306", "3306", true},
		{"127.0.0.1:5432", "5432", false},
		{"[::]:5432", "5432", true},
		{"[::1]:5432", "5432", false},
		{":::5432", "5432", true},
		{"203.0.113.5:5432", "5432", true},
		{"0.0.0.0:3306", "5432", false}, // different port
		{"127.0.0.1:6379", "6379", false},
		{"0.0.0.0:6379", "6379", true},
	}
	for _, tc := range cases {
		got := secwatch.IsPublicBind(tc.addrPort, tc.port)
		if got != tc.exposed {
			t.Errorf("IsPublicBind(%q, %q) = %v, want %v",
				tc.addrPort, tc.port, got, tc.exposed)
		}
	}
}

// ---------------------------------------------------------------------------
// parseUFWStatus
// ---------------------------------------------------------------------------

func TestParseUFWStatus_ActiveWithPorts(t *testing.T) {
	out := `Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
80/tcp                     ALLOW       Anywhere
443/tcp                    ALLOW       Anywhere
22/tcp (v6)                ALLOW       Anywhere (v6)
80/tcp (v6)                ALLOW       Anywhere (v6)
443/tcp (v6)               ALLOW       Anywhere (v6)
`
	fs := secwatch.ParseUFWStatus(out)
	if !fs.Active {
		t.Error("expected Active=true")
	}
	if !fs.Available {
		t.Error("expected Available=true")
	}
	for _, port := range []int{22, 80, 443} {
		if !fs.AllowedPorts[port] {
			t.Errorf("expected port %d to be in AllowedPorts", port)
		}
	}
	if fs.AllowedPorts[8080] {
		t.Error("expected port 8080 NOT in AllowedPorts")
	}
}

func TestParseUFWStatus_Inactive(t *testing.T) {
	out := "Status: inactive\n"
	fs := secwatch.ParseUFWStatus(out)
	if fs.Active {
		t.Error("expected Active=false for inactive UFW")
	}
	if len(fs.AllowedPorts) != 0 {
		t.Errorf("expected no allowed ports for inactive UFW, got %v", fs.AllowedPorts)
	}
}

func TestParseUFWStatus_CustomPort(t *testing.T) {
	out := `Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
80/tcp                     ALLOW       Anywhere
443/tcp                    ALLOW       Anywhere
3000/tcp                   ALLOW       Anywhere
3000/tcp (v6)              ALLOW       Anywhere (v6)
`
	fs := secwatch.ParseUFWStatus(out)
	if !fs.AllowedPorts[3000] {
		t.Error("expected port 3000 in AllowedPorts")
	}
}

// ---------------------------------------------------------------------------
// parseFail2banStatus
// ---------------------------------------------------------------------------

func TestParseFail2banStatus_WithBannedIPs(t *testing.T) {
	out := `Status for the jail: sshd
|- Filter
|  |- Currently failed: 3
|  |- Total failed: 47
|  ` + "`" + `- File list:   /var/log/auth.log
` + "`" + `- Actions
   |- Currently banned: 2
   |- Total banned: 5
   ` + "`" + `- Banned IP list:  1.2.3.4 5.6.7.8
`
	fs := secwatch.ParseFail2banStatus(out)
	if !fs.Available {
		t.Error("expected Available=true")
	}
	if !fs.Active {
		t.Error("expected Active=true")
	}
	if fs.TotalFailed != 47 {
		t.Errorf("TotalFailed = %d, want 47", fs.TotalFailed)
	}
	if len(fs.BannedIPs) != 2 {
		t.Errorf("BannedIPs = %v, want [1.2.3.4 5.6.7.8]", fs.BannedIPs)
	}
	if fs.BannedIPs[0] != "1.2.3.4" || fs.BannedIPs[1] != "5.6.7.8" {
		t.Errorf("BannedIPs = %v, want [1.2.3.4 5.6.7.8]", fs.BannedIPs)
	}
}

func TestParseFail2banStatus_NoBans(t *testing.T) {
	out := `Status for the jail: sshd
|- Filter
|  |- Currently failed: 0
|  |- Total failed: 0
|  ` + "`" + `- File list:   /var/log/auth.log
` + "`" + `- Actions
   |- Currently banned: 0
   |- Total banned: 0
   ` + "`" + `- Banned IP list:
`
	fs := secwatch.ParseFail2banStatus(out)
	if fs.TotalFailed != 0 {
		t.Errorf("TotalFailed = %d, want 0", fs.TotalFailed)
	}
	if len(fs.BannedIPs) != 0 {
		t.Errorf("BannedIPs should be empty, got %v", fs.BannedIPs)
	}
}

// ---------------------------------------------------------------------------
// parseJournaldSSH
// ---------------------------------------------------------------------------

func TestParseJournaldSSH_AcceptedLogins(t *testing.T) {
	// Mix both timestamp formats:
	//   +00:00  — colon form emitted by systemd 255+ (Ubuntu 24.04)
	//   +0000   — no-colon form emitted by older systemd
	out := `2026-06-24T10:00:00+00:00 hostname sshd[1234]: Accepted publickey for ubuntu from 192.168.1.100 port 12345 ssh2: RSA SHA256:abc
2026-06-24T10:01:00+0000 hostname sshd[1235]: Accepted password for admin from 10.0.0.5 port 54321 ssh2
2026-06-24T10:02:00+00:00 hostname sshd[1236]: Failed password for invalid user root from 203.0.113.1 port 22222 ssh2
2026-06-24T10:03:00+0000 hostname sshd[1237]: Invalid user foo from 203.0.113.1 port 33333
`
	logins, failed := secwatch.ParseJournaldSSH(out)

	if len(logins) != 2 {
		t.Fatalf("want 2 logins, got %d: %+v", len(logins), logins)
	}
	if failed != 2 {
		t.Errorf("want 2 failed attempts, got %d", failed)
	}

	first := logins[0]
	if first.User != "ubuntu" {
		t.Errorf("User = %q, want ubuntu", first.User)
	}
	if first.SourceIP != "192.168.1.100" {
		t.Errorf("SourceIP = %q, want 192.168.1.100", first.SourceIP)
	}
	if first.Method != "publickey" {
		t.Errorf("Method = %q, want publickey", first.Method)
	}

	second := logins[1]
	if second.Method != "password" {
		t.Errorf("Method = %q, want password", second.Method)
	}
}

func TestParseJournaldSSH_Empty(t *testing.T) {
	logins, failed := secwatch.ParseJournaldSSH("")
	if len(logins) != 0 || failed != 0 {
		t.Errorf("empty input: got %d logins, %d failed", len(logins), failed)
	}
}

// ---------------------------------------------------------------------------
// ExpectedAllowlist
// ---------------------------------------------------------------------------

func TestExpectedAllowlist_Defaults(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		Core: schema.CoreConfig{
			Caddy: schema.CaddyCoreConfig{Email: "admin@example.com"},
		},
	}
	list := secwatch.ExpectedAllowlist(cfg, 22)
	if !list[22] {
		t.Error("SSH port 22 should be expected")
	}
	if !list[80] || !list[443] {
		t.Error("ports 80 and 443 should always be expected")
	}
}

func TestExpectedAllowlist_CustomSSHPort(t *testing.T) {
	list := secwatch.ExpectedAllowlist(nil, 2222)
	if !list[2222] {
		t.Error("custom SSH port 2222 should be expected")
	}
	if list[22] {
		t.Error("default SSH port 22 should NOT be expected when using custom port")
	}
}

// ---------------------------------------------------------------------------
// ComputeExposure
// ---------------------------------------------------------------------------

func TestComputeExposure_LoopbackAlwaysSafe(t *testing.T) {
	listeners := []secwatch.Listener{
		{Port: 7070, Proto: "tcp", Bind: "127.0.0.1"},
		{Port: 5432, Proto: "tcp", Bind: "127.0.0.1"},
	}
	fw := secwatch.FirewallState{Available: true, Active: true, AllowedPorts: map[int]bool{22: true, 80: true, 443: true}}
	allowlist := map[int]bool{22: true, 80: true, 443: true}

	result := secwatch.ComputeExposure(listeners, fw, allowlist)

	if result.UnexpectedCount != 0 {
		t.Errorf("UnexpectedCount = %d, want 0 (loopback listeners are always safe)", result.UnexpectedCount)
	}
	for _, l := range result.Listeners {
		if l.InternetReachable {
			t.Errorf("loopback listener %d marked internet-reachable", l.Port)
		}
	}
}

func TestComputeExposure_UnexpectedPort(t *testing.T) {
	listeners := []secwatch.Listener{
		{Port: 22, Proto: "tcp", Bind: "0.0.0.0"},
		{Port: 80, Proto: "tcp", Bind: "0.0.0.0"},
		{Port: 443, Proto: "tcp", Bind: "0.0.0.0"},
		// This one is internet-reachable but not in the allowlist.
		{Port: 8080, Proto: "tcp", Bind: "0.0.0.0"},
	}
	fw := secwatch.FirewallState{
		Available:    true,
		Active:       true,
		AllowedPorts: map[int]bool{22: true, 80: true, 443: true, 8080: true},
	}
	allowlist := map[int]bool{22: true, 80: true, 443: true}

	result := secwatch.ComputeExposure(listeners, fw, allowlist)

	if result.UnexpectedCount != 1 {
		t.Errorf("UnexpectedCount = %d, want 1", result.UnexpectedCount)
	}
	// Find the 8080 listener.
	var found *secwatch.Listener
	for i := range result.Listeners {
		if result.Listeners[i].Port == 8080 {
			found = &result.Listeners[i]
		}
	}
	if found == nil {
		t.Fatal("listener on port 8080 not in result")
	}
	if !found.InternetReachable {
		t.Error("port 8080 should be internet-reachable")
	}
	if found.Expected {
		t.Error("port 8080 should not be expected")
	}
}

func TestComputeExposure_PublicPortNotInFirewall(t *testing.T) {
	// A port binding 0.0.0.0 but NOT in UFW allowed ports is NOT internet-reachable.
	listeners := []secwatch.Listener{
		{Port: 9999, Proto: "tcp", Bind: "0.0.0.0"},
	}
	fw := secwatch.FirewallState{
		Available:    true,
		Active:       true,
		AllowedPorts: map[int]bool{22: true, 80: true, 443: true},
	}
	allowlist := map[int]bool{22: true, 80: true, 443: true}

	result := secwatch.ComputeExposure(listeners, fw, allowlist)
	if result.UnexpectedCount != 0 {
		t.Errorf("UnexpectedCount = %d, want 0 (blocked by firewall)", result.UnexpectedCount)
	}
	for _, l := range result.Listeners {
		if l.Port == 9999 && l.InternetReachable {
			t.Error("port 9999 should not be internet-reachable (blocked by UFW)")
		}
	}
}

// ---------------------------------------------------------------------------
// Gather round-trip: zero-value ExposureResult stays Available=false
// ---------------------------------------------------------------------------

func TestGatherExposure_UnavailableWhenNoTools(t *testing.T) {
	// On macOS (dev box) or any host without ss+ufw, GatherExposure should
	// return Available=false rather than erroring.
	//
	// We can't test this directly without mocking exec.LookPath, but we can
	// verify that a zero-value result is correctly represented.
	var result secwatch.ExposureResult
	if result.Available {
		t.Error("zero ExposureResult should have Available=false")
	}
	if result.UnexpectedCount != 0 {
		t.Error("zero ExposureResult should have UnexpectedCount=0")
	}
}

// ---------------------------------------------------------------------------
// parseSSLine (internal, tested via exported ParseSSLineForTest)
// ---------------------------------------------------------------------------

func TestParseSSLine_ValidLines(t *testing.T) {
	cases := []struct {
		line     string
		proto    string
		wantPort int
		wantBind string
		wantProc string
	}{
		{
			`LISTEN 0      128    0.0.0.0:22      0.0.0.0:*    users:(("sshd",pid=812,fd=3))`,
			"tcp", 22, "0.0.0.0", "sshd",
		},
		{
			`LISTEN 0      511    127.0.0.1:7070  0.0.0.0:*    users:(("ownbase-a",pid=1234,fd=8))`,
			"tcp", 7070, "127.0.0.1", "ownbase-a",
		},
		{
			`LISTEN 0      4096   [::]:80         [::]:*       users:(("caddy",pid=5678,fd=9))`,
			"tcp", 80, "::", "caddy",
		},
		{
			`LISTEN 0      128    [::1]:5432      [::]:*`,
			"tcp", 5432, "::1", "",
		},
	}
	for _, tc := range cases {
		l, ok := secwatch.ParseSSLineForTest(tc.line, tc.proto)
		if !ok {
			t.Errorf("ParseSSLine(%q) returned ok=false", tc.line)
			continue
		}
		if l.Port != tc.wantPort {
			t.Errorf("Port = %d, want %d (line: %q)", l.Port, tc.wantPort, tc.line)
		}
		if l.Bind != tc.wantBind {
			t.Errorf("Bind = %q, want %q (line: %q)", l.Bind, tc.wantBind, tc.line)
		}
		if l.Process != tc.wantProc {
			t.Errorf("Process = %q, want %q (line: %q)", l.Process, tc.wantProc, tc.line)
		}
	}
}

func TestParseSSLine_InvalidLines(t *testing.T) {
	bad := []string{
		"",
		"only three fields here",
		"State Recv-Q Send-Q", // missing addr
	}
	for _, line := range bad {
		_, ok := secwatch.ParseSSLineForTest(line, "tcp")
		if ok {
			t.Errorf("expected ParseSSLine(%q) to return ok=false", line)
		}
	}
}

// ---------------------------------------------------------------------------
// GatherAccess: zero-value stays Available=false
// ---------------------------------------------------------------------------

func TestAccessResult_ZeroValue(t *testing.T) {
	var result secwatch.AccessResult
	if result.Available {
		t.Error("zero AccessResult should have Available=false")
	}
	if len(result.BannedIPs) != 0 {
		t.Error("zero AccessResult should have empty BannedIPs")
	}
	if strings.Contains(strings.Join(result.BannedIPs, ""), "nil") {
		t.Error("unexpected nil in BannedIPs")
	}
}
