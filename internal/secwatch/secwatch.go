// Package secwatch implements local network-exposure scanning and SSH access
// monitoring for OwnBase.
//
// Two probes run after every reconcile:
//
//	GatherExposure — cross-references listening sockets (ss -tlnpH) with
//	UFW rules and the expected-allowlist derived from ownbase.yaml to
//	produce an authoritative inventory of what this machine believes is
//	internet-reachable, and whether each listener is expected.
//
//	GatherAccess — reads fail2ban status and recent SSH auth events via
//	journald to surface brute-force activity without adding a new daemon.
//
// Both probes degrade gracefully: when a tool is absent (e.g. macOS dev
// environment), the result carries Available=false and zero values so
// Tier-1 tests pass off-VM.
//
// Honesty caveat: the inventory answers "what does this machine believe is
// reachable?" It cannot see an upstream cloud-provider firewall or a
// kernel-level rootkit hiding sockets. External verification is a future
// augmentation, never part of the verdict.
package secwatch

import (
	"time"
)

// ExposureResult is the result of a network exposure scan.
type ExposureResult struct {
	// Available is false when the required tools (ss, ufw) are not installed.
	// When false all other fields are zero. The agent treats this as "unknown"
	// rather than "secure" — a missing tool is not a pass.
	Available bool `json:"available"`

	// FirewallActive is true when UFW reports "Status: active".
	FirewallActive bool `json:"firewall_active"`

	// UnexpectedCount is the number of internet-reachable listeners that are
	// not in the expected allowlist. Any non-zero value is a warning signal.
	UnexpectedCount int `json:"unexpected_count"`

	// Listeners is the full inventory of listening sockets with their
	// internet-reachability and expected-allowlist status.
	Listeners []Listener `json:"listeners,omitempty"`
}

// Listener represents a single listening socket and its security assessment.
type Listener struct {
	Port    int    `json:"port"`
	Proto   string `json:"proto"`             // "tcp" or "udp"
	Bind    string `json:"bind"`              // cleaned bind address, e.g. "0.0.0.0", "127.0.0.1", "::"
	Process string `json:"process,omitempty"` // process name if available (requires root for ss -p)

	// InternetReachable is true when the socket binds a non-loopback address
	// AND UFW allows that port inbound. Loopback-only sockets are always safe.
	InternetReachable bool `json:"internet_reachable"`

	// Expected is true when the port appears in the expected-allowlist derived
	// from ownbase.yaml (SSH, 80, 443, Forgejo when no domain is set).
	Expected bool `json:"expected"`
}

// AccessResult is the result of an SSH access monitor probe.
type AccessResult struct {
	// Available is false when fail2ban-client and/or journalctl are absent.
	Available bool `json:"available"`

	// Fail2banAvailable is true when fail2ban-client was reachable and the
	// sshd jail was successfully queried. When false, Fail2banActive and
	// BannedIPs are unknown (not necessarily "inactive"); journald data may
	// still be present if journalctl was available.
	Fail2banAvailable bool `json:"fail2ban_available"`

	// Fail2banActive is true when fail2ban is running and the SSH jail is
	// configured. Only meaningful when Fail2banAvailable is true.
	Fail2banActive bool `json:"fail2ban_active"`

	// BannedIPs is the currently active ban list for the SSH jail.
	BannedIPs []string `json:"banned_ips,omitempty"`

	// FailedAttempts is the total number of failed auth attempts seen by
	// fail2ban in the current fail2ban session.
	FailedAttempts int `json:"failed_attempts"`

	// RecentLogins is the list of successful SSH logins in the monitoring
	// window (default 24 h). Empty when journalctl is unavailable.
	RecentLogins []LoginEvent `json:"recent_logins,omitempty"`
}

// LoginEvent is one successful SSH login from the auth log.
type LoginEvent struct {
	Time     time.Time `json:"time"`
	User     string    `json:"user"`
	SourceIP string    `json:"source_ip"`
	Method   string    `json:"method,omitempty"` // e.g. "publickey", "password"
}
